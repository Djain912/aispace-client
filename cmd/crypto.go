package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"filippo.io/age"
	"github.com/spf13/cobra"

	"github.com/aispace-sh/aispace-client/internal/api"
)

const (
	clientEncryptionAlgorithm = "age-x25519"
	encryptedContentType      = "application/vnd.aispace.age"
	identityEnv               = "AISPACE_AGE_IDENTITY"
)

type encryptedUpload struct {
	Reader       io.ReadSeeker
	Size         int64
	Recipient    string
	Identity     string
	IdentityFile string
	OriginalName string
	cleanup      func()
}

type encryptionOutput struct {
	Algorithm    string `json:"algorithm"`
	Recipient    string `json:"recipient"`
	Identity     string `json:"identity,omitempty"`
	IdentityFile string `json:"identity_file,omitempty"`
	OriginalName string `json:"original_name"`
}

type keygenOutput struct {
	Algorithm    string `json:"algorithm"`
	Recipient    string `json:"recipient"`
	Identity     string `json:"identity,omitempty"`
	IdentityFile string `json:"identity_file,omitempty"`
}

func (a *app) keygenCmd() *cobra.Command {
	var identityOut string
	cmd := &cobra.Command{
		Use:   "keygen [--identity-out PATH]",
		Short: "Generate an age X25519 identity and recipient locally",
		Long: "Generates an age X25519 key pair locally without contacting aispace.\n" +
			"With --identity-out, the secret identity is saved to a new mode-0600 file\n" +
			"and is not printed; the public recipient is always printed.",
		Args: noArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			identity, err := age.GenerateX25519Identity()
			if err != nil {
				return &codedError{code: "keygen", err: fmt.Errorf("generate age identity: %w", err), exit: ExitGeneric}
			}
			out := keygenOutput{
				Algorithm: clientEncryptionAlgorithm,
				Recipient: identity.Recipient().String(),
				Identity:  identity.String(),
			}
			if identityOut != "" {
				if err := writeSecretFile(identityOut, out.Identity+"\n"); err != nil {
					return err
				}
				out.Identity = ""
				out.IdentityFile = identityOut
			}
			if a.jsonOut {
				return a.printJSONValue(out)
			}
			fmt.Fprintf(a.stdout, "recipient %s\n", out.Recipient)
			if out.IdentityFile != "" {
				fmt.Fprintf(a.stdout, "identity saved %s\n", out.IdentityFile)
			} else {
				fmt.Fprintf(a.stdout, "identity %s\n", out.Identity)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&identityOut, "identity-out", "", "save the identity to a new mode-0600 file instead of printing it")
	return cmd
}

func encryptForUpload(src io.Reader, originalName, recipientText, identityOut string) (*encryptedUpload, error) {
	var recipient age.Recipient
	var identity string
	if recipientText != "" {
		parsed, err := age.ParseX25519Recipient(strings.TrimSpace(recipientText))
		if err != nil {
			return nil, usagef("--recipient must be a valid age X25519 recipient: %v", err)
		}
		recipient = parsed
		recipientText = parsed.String()
	} else {
		generated, err := age.GenerateX25519Identity()
		if err != nil {
			return nil, &codedError{code: "encryption", err: fmt.Errorf("generate decryption identity: %w", err), exit: ExitGeneric}
		}
		recipient = generated.Recipient()
		recipientText = generated.Recipient().String()
		identity = generated.String()
	}

	if identityOut != "" {
		if err := writeSecretFile(identityOut, identity+"\n"); err != nil {
			return nil, err
		}
	}

	tmp, err := os.CreateTemp("", "aispace-encrypted-*.age")
	if err != nil {
		return nil, &codedError{code: "io", err: fmt.Errorf("create encryption temp file: %w", err), exit: ExitGeneric}
	}
	cleanup := func() {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
	}
	if err := tmp.Chmod(0o600); err != nil {
		cleanup()
		return nil, &codedError{code: "io", err: fmt.Errorf("secure encryption temp file: %w", err), exit: ExitGeneric}
	}
	w, err := age.Encrypt(tmp, recipient)
	if err != nil {
		cleanup()
		return nil, &codedError{code: "encryption", err: fmt.Errorf("start encryption: %w", err), exit: ExitGeneric}
	}
	if _, err := io.Copy(w, src); err != nil {
		_ = w.Close()
		cleanup()
		return nil, &codedError{code: "io", err: fmt.Errorf("encrypt %s: %w", originalName, err), exit: ExitGeneric}
	}
	if err := w.Close(); err != nil {
		cleanup()
		return nil, &codedError{code: "encryption", err: fmt.Errorf("finish encryption: %w", err), exit: ExitGeneric}
	}
	st, err := tmp.Stat()
	if err != nil {
		cleanup()
		return nil, &codedError{code: "io", err: fmt.Errorf("stat encrypted file: %w", err), exit: ExitGeneric}
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		cleanup()
		return nil, &codedError{code: "io", err: fmt.Errorf("rewind encrypted file: %w", err), exit: ExitGeneric}
	}
	return &encryptedUpload{
		Reader:       tmp,
		Size:         st.Size(),
		Recipient:    recipientText,
		Identity:     identity,
		IdentityFile: identityOut,
		OriginalName: originalName,
		cleanup:      cleanup,
	}, nil
}

func writeSecretFile(path, value string) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return usagef("identity file already exists: %s", path)
		}
		return &codedError{code: "io", err: fmt.Errorf("create identity file: %w", err), exit: ExitGeneric}
	}
	ok := false
	defer func() {
		_ = f.Close()
		if !ok {
			_ = os.Remove(path)
		}
	}()
	if _, err := io.WriteString(f, value); err != nil {
		return &codedError{code: "io", err: fmt.Errorf("write identity file: %w", err), exit: ExitGeneric}
	}
	if err := f.Close(); err != nil {
		return &codedError{code: "io", err: fmt.Errorf("close identity file: %w", err), exit: ExitGeneric}
	}
	ok = true
	return nil
}

func (e *encryptedUpload) output() encryptionOutput {
	identity := e.Identity
	if e.IdentityFile != "" {
		identity = ""
	}
	return encryptionOutput{
		Algorithm:    clientEncryptionAlgorithm,
		Recipient:    e.Recipient,
		Identity:     identity,
		IdentityFile: e.IdentityFile,
		OriginalName: e.OriginalName,
	}
}

func (a *app) printEncryption(e *encryptedUpload) {
	fmt.Fprintf(a.stdout, "encryption %s recipient %s\n", clientEncryptionAlgorithm, e.Recipient)
	if e.IdentityFile != "" {
		fmt.Fprintf(a.stdout, "decryption_identity saved %s\n", e.IdentityFile)
	} else if e.Identity != "" {
		fmt.Fprintf(a.stdout, "decryption_identity %s\n", e.Identity)
	}
}

type decryptFlags struct {
	identityFile string
	output       string
}

func (a *app) decryptCmd() *cobra.Command {
	var f decryptFlags
	cmd := &cobra.Command{
		Use:   "decrypt <path|url|-> --output <path|-> [--identity-file PATH]",
		Short: "Decrypt an age-encrypted file locally",
		Long: "Decrypts a local file, stdin, or an aispace share URL without sending the\n" +
			"decryption identity to aispace. Read the identity from --identity-file or\n" +
			identityEnv + ". Output files are created with mode 0600 and never overwritten.",
		Example: "  aispace decrypt report.pdf.age --identity-file report.key --output report.pdf\n" +
			"  AISPACE_AGE_IDENTITY=AGE-SECRET-KEY-... aispace decrypt https://aispace.sh/d/... --output report.pdf",
		Args: exactArgs(1, "<path|url|->"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.runDecrypt(cmd.Context(), args[0], f)
		},
	}
	cmd.Flags().StringVar(&f.identityFile, "identity-file", "", "file containing an age X25519 identity (otherwise use "+identityEnv+")")
	cmd.Flags().StringVarP(&f.output, "output", "o", "", "plaintext output path, or - for stdout (required for URL and stdin)")
	return cmd
}

func (a *app) runDecrypt(ctx context.Context, input string, f decryptFlags) error {
	if f.output == "" {
		if input == "-" || isHTTPURL(input) {
			return usagef("--output is required when decrypting a URL or stdin")
		}
		if strings.HasSuffix(strings.ToLower(input), ".age") {
			f.output = input[:len(input)-len(".age")]
		} else {
			f.output = input + ".decrypted"
		}
	}
	if f.output == "-" && a.jsonOut {
		return usagef("--json cannot be combined with --output -")
	}

	identity, err := a.loadIdentity(f.identityFile)
	if err != nil {
		return err
	}
	src, closeSource, err := a.openEncryptedInput(ctx, input)
	if err != nil {
		return err
	}
	defer closeSource()
	plaintext, err := age.Decrypt(src, identity)
	if err != nil {
		return &codedError{code: "decryption", err: fmt.Errorf("decrypt header: %w", err), exit: ExitGeneric}
	}

	if f.output == "-" {
		if _, err := io.Copy(a.stdout, plaintext); err != nil {
			return &codedError{code: "decryption", err: fmt.Errorf("decrypt body: %w", err), exit: ExitGeneric}
		}
		return nil
	}
	if samePath(input, f.output) {
		return usagef("output must not overwrite the encrypted input")
	}
	out, err := os.OpenFile(f.output, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return usagef("output file already exists: %s", f.output)
		}
		return &codedError{code: "io", err: fmt.Errorf("create output: %w", err), exit: ExitGeneric}
	}
	ok := false
	defer func() {
		_ = out.Close()
		if !ok {
			_ = os.Remove(f.output)
		}
	}()
	if _, err := io.Copy(out, plaintext); err != nil {
		return &codedError{code: "decryption", err: fmt.Errorf("decrypt body: %w", err), exit: ExitGeneric}
	}
	if err := out.Close(); err != nil {
		return &codedError{code: "io", err: fmt.Errorf("close output: %w", err), exit: ExitGeneric}
	}
	ok = true
	if a.jsonOut {
		return a.printJSONValue(struct {
			Source string `json:"source"`
			Output string `json:"output"`
		}{Source: input, Output: f.output})
	}
	fmt.Fprintf(a.stdout, "decrypted %s -> %s\n", input, f.output)
	return nil
}

func (a *app) loadIdentity(path string) (age.Identity, error) {
	var raw string
	if path != "" {
		// Windows has no Unix permission bits: os.Stat reports 0666 for every
		// file, so this would warn on every decrypt and tell the user to run a
		// command they do not have. config.Load skips its own permission
		// warning there for the same reason.
		if runtime.GOOS != "windows" {
			if info, err := os.Stat(path); err == nil && info.Mode().Perm()&0o077 != 0 {
				fmt.Fprintf(a.stderr, "warning: identity file %s has permissions %04o, expected 0600 (run: chmod 600 %s)\n", path, info.Mode().Perm(), path)
			}
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return nil, &codedError{code: "io", err: fmt.Errorf("read identity file: %w", err), exit: ExitGeneric}
		}
		raw = string(b)
	} else {
		raw = os.Getenv(identityEnv)
		if raw == "" {
			return nil, usagef("no decryption identity: use --identity-file or set %s", identityEnv)
		}
	}
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		identity, err := age.ParseX25519Identity(line)
		if err != nil {
			return nil, usagef("invalid age X25519 identity: %v", err)
		}
		return identity, nil
	}
	return nil, usagef("identity file contains no age X25519 identity")
}

func (a *app) openEncryptedInput(ctx context.Context, input string) (io.Reader, func(), error) {
	if input == "-" {
		return a.stdin, func() {}, nil
	}
	if isHTTPURL(input) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, input, nil)
		if err != nil {
			return nil, func() {}, usagef("invalid URL: %v", err)
		}
		req.Header.Set("User-Agent", a.userAgent())
		resp, err := api.NewHTTPClient().Do(req)
		if err != nil {
			return nil, func() {}, &codedError{code: "download", err: err, exit: ExitGeneric}
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			_ = resp.Body.Close()
			return nil, func() {}, &codedError{code: "download", err: fmt.Errorf("download failed: HTTP %d", resp.StatusCode), exit: ExitGeneric}
		}
		return resp.Body, func() { _ = resp.Body.Close() }, nil
	}
	f, err := os.Open(input)
	if err != nil {
		return nil, func() {}, &codedError{code: "io", err: err, exit: ExitGeneric}
	}
	return f, func() { _ = f.Close() }, nil
}

func isHTTPURL(value string) bool {
	u, err := url.Parse(value)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
}

func samePath(input, output string) bool {
	if input == "-" || isHTTPURL(input) {
		return false
	}
	inAbs, inErr := filepath.Abs(input)
	outAbs, outErr := filepath.Abs(output)
	return inErr == nil && outErr == nil && inAbs == outAbs
}

func encryptionJSON(file json.RawMessage, link json.RawMessage, encrypted *encryptedUpload) json.RawMessage {
	value := struct {
		File       json.RawMessage  `json:"file"`
		Link       json.RawMessage  `json:"link,omitempty"`
		Encryption encryptionOutput `json:"encryption"`
	}{File: file, Link: link, Encryption: encrypted.output()}
	return mustJSON(value)
}
