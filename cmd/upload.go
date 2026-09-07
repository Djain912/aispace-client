package cmd

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/aispace-sh/aispace-client/internal/api"
	"github.com/aispace-sh/aispace-client/internal/duration"
)

type uploadFlags struct {
	name         string
	expires      string
	link         bool
	linkExpires  string
	maxDownloads int64
	sha256       bool
	contentType  string
	encrypt      bool
	recipient    string
	identityOut  string
	private      bool
	shared       bool
}

func (a *app) uploadCmd() *cobra.Command {
	var f uploadFlags
	cmd := &cobra.Command{
		Use:   "upload <path|-> [--name N] [--expires 24h] [--link] [--encrypt] [--recipient age1...]",
		Short: "Upload a file (or stdin with -) and optionally mint a share link",
		Long: "Uploads a file with a raw streaming POST /v1/files. Use - to read stdin\n" +
			"(buffered to a temp file so Content-Length is known). With --link a share link is\n" +
			"created right after and its URL is printed last on its own line. --encrypt uses\n" +
			"age X25519 locally; aispace receives ciphertext and never the decryption identity.",
		Example: "  aispace upload report.pdf --expires 7d --link --link-expires 1h --max-downloads 3\n" +
			"  aispace upload secret.pdf --encrypt --identity-out secret.key --link\n" +
			"  aispace upload secret.pdf --encrypt --recipient age1... --link\n" +
			"  echo hi | aispace upload - --name note.txt --link\n" +
			"  aispace upload data.csv --json",
		Args: exactArgs(1, "<path|->"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.runUpload(cmd, args[0], f)
		},
	}
	cmd.Flags().StringVar(&f.name, "name", "", "file name to store (default: basename of path, or \"stdin\" for -)")
	cmd.Flags().StringVar(&f.expires, "expires", "", "file lifetime, e.g. 30m, 24h, 7d (default: server default, 7d)")
	cmd.Flags().BoolVar(&f.link, "link", false, "create a Pro public share link after upload and print its URL last")
	cmd.Flags().StringVar(&f.linkExpires, "link-expires", "", "share link lifetime, e.g. 1h (requires --link; default: server default, 1h)")
	cmd.Flags().Int64Var(&f.maxDownloads, "max-downloads", 0, "share link download cap (requires --link; default: unlimited)")
	cmd.Flags().StringVar(&f.contentType, "content-type", "", "MIME type to store (default: guessed from the file extension)")
	cmd.Flags().BoolVar(&f.sha256, "sha256", false, "compute SHA-256 locally and send X-SHA256 for server-side verification")
	cmd.Flags().BoolVar(&f.encrypt, "encrypt", false, "encrypt locally with age X25519 before upload")
	cmd.Flags().StringVar(&f.recipient, "recipient", "", "existing age X25519 recipient (requires --encrypt; otherwise a one-time identity is generated)")
	cmd.Flags().StringVar(&f.identityOut, "identity-out", "", "save the generated decryption identity to a new mode-0600 file (requires --encrypt)")
	cmd.Flags().BoolVar(&f.private, "private", false, "keep this upload private to the current key")
	cmd.Flags().BoolVar(&f.shared, "shared", false, "share this upload with every key on the account")
	return cmd
}

func (a *app) runUpload(cmd *cobra.Command, path string, f uploadFlags) error {
	if f.maxDownloads < 0 {
		return usagef("--max-downloads must be >= 0")
	}
	if !f.link && (f.linkExpires != "" || f.maxDownloads > 0) {
		return usagef("--link-expires and --max-downloads require --link")
	}
	if !f.encrypt && (f.recipient != "" || f.identityOut != "") {
		return usagef("--recipient and --identity-out require --encrypt")
	}
	if f.recipient != "" && f.identityOut != "" {
		return usagef("--identity-out cannot be used with --recipient because the recipient owns the identity")
	}
	if f.private && f.shared {
		return usagef("--private and --shared are mutually exclusive")
	}
	var expiresIn, linkExpiresIn int64
	var err error
	if f.expires != "" {
		if expiresIn, err = duration.Seconds(f.expires); err != nil {
			return usagef("--expires: %v", err)
		}
	}
	if f.linkExpires != "" {
		if linkExpiresIn, err = duration.Seconds(f.linkExpires); err != nil {
			return usagef("--link-expires: %v", err)
		}
	}
	c, err := a.client()
	if err != nil {
		return err
	}

	originalName := resolvedUploadName(path, f.name)
	name := originalName
	ct := f.contentType
	if ct == "" {
		ct = contentTypeFor(name)
	}
	if f.encrypt {
		if !strings.HasSuffix(strings.ToLower(name), ".age") {
			name += ".age"
		}
		ct = encryptedContentType
	}
	if err := validateHeaderValue("file name", name); err != nil {
		return err
	}
	if err := validateHeaderValue("--content-type", ct); err != nil {
		return err
	}

	src, size, _, cleanup, err := a.openSource(path, f.name)
	if err != nil {
		return err
	}
	defer cleanup()

	var encrypted *encryptedUpload
	if f.encrypt {
		encrypted, err = encryptForUpload(src, originalName, f.recipient, f.identityOut)
		if err != nil {
			return err
		}
		defer encrypted.cleanup()
		src = encrypted.Reader
		size = encrypted.Size
	}

	opts := api.UploadOptions{Name: name, Size: size, ExpiresIn: expiresIn, ContentType: ct}
	if f.private {
		opts.Visibility = "private"
	} else if f.shared {
		opts.Visibility = "account"
	}
	if encrypted != nil {
		opts.Encryption = clientEncryptionAlgorithm
	}
	if f.sha256 || encrypted != nil {
		sum, err := hashAndRewind(src)
		if err != nil {
			return &codedError{code: "io", err: fmt.Errorf("hash %s: %w", name, err), exit: ExitGeneric}
		}
		opts.SHA256 = sum
	}

	fileRes, err := c.Upload(cmd.Context(), src, opts)
	if err != nil {
		if encrypted != nil {
			a.discardUnusedIdentity(encrypted, err)
		}
		return err
	}
	file := fileRes.Value

	if !f.link {
		if a.jsonOut {
			if encrypted != nil {
				a.printJSON(encryptionJSON(fileRes.Raw, nil, encrypted))
			} else {
				a.printJSON(fileRes.Raw)
			}
			return nil
		}
		a.printFileLine("uploaded", file)
		if encrypted != nil {
			a.printEncryption(encrypted)
		}
		return nil
	}

	linkRes, err := c.CreateLink(cmd.Context(), file.ID, api.LinkOptions{ExpiresIn: linkExpiresIn, MaxDownloads: f.maxDownloads})
	if err != nil {
		// The file is stored; tell the caller so it is not lost.
		if a.jsonOut {
			if encrypted != nil {
				a.printJSON(encryptionJSON(fileRes.Raw, nil, encrypted))
			} else {
				a.printJSON(mustJSON(map[string]json.RawMessage{"file": fileRes.Raw}))
			}
		} else {
			a.printFileLine("uploaded", file)
			if encrypted != nil {
				a.printEncryption(encrypted)
			}
		}
		return err
	}
	if a.jsonOut {
		if encrypted != nil {
			a.printJSON(encryptionJSON(fileRes.Raw, linkRes.Raw, encrypted))
		} else {
			a.printJSON(mustJSON(map[string]json.RawMessage{"file": fileRes.Raw, "link": linkRes.Raw}))
		}
		return nil
	}
	a.printFileLine("uploaded", file)
	if encrypted != nil {
		a.printEncryption(encrypted)
	}
	a.printLinkLine(linkRes.Value)
	fmt.Fprintln(a.stdout, linkRes.Value.URL)
	return nil
}

// discardUnusedIdentity removes an identity file written for an upload the
// server refused. Nothing was stored, so the identity decrypts nothing, and
// leaving it behind makes the obvious retry of the same command fail with
// "identity file already exists" — a usage error for arguments that were right.
//
// A transport or server failure is different: the file may have been stored
// even though no success response reached the client. Those identities are
// kept, and the caller is told why.
func (a *app) discardUnusedIdentity(e *encryptedUpload, err error) {
	if e.IdentityFile == "" {
		return
	}
	var ae *api.Error
	if errors.As(err, &ae) && ae.Status >= 400 && ae.Status < 500 {
		// The server rejected the request, so no ciphertext exists.
		if os.Remove(e.IdentityFile) == nil {
			return
		}
	}
	fmt.Fprintf(a.stderr, "warning: kept identity file %s; the upload may still have stored the file, so check `aispace ls` before removing it\n", e.IdentityFile)
}

func resolvedUploadName(path, nameFlag string) string {
	if nameFlag != "" {
		return nameFlag
	}
	if path == "-" {
		return "stdin"
	}
	return filepath.Base(path)
}

// openSource returns a seekable reader for path (or stdin buffered to a temp
// file), its size, the name to send and a cleanup func.
func (a *app) openSource(path, nameFlag string) (io.ReadSeeker, int64, string, func(), error) {
	noop := func() {}
	if path == "-" {
		tmp, err := os.CreateTemp("", "aispace-upload-*")
		if err != nil {
			return nil, 0, "", noop, &codedError{code: "io", err: fmt.Errorf("create temp file: %w", err), exit: ExitGeneric}
		}
		cleanup := func() { tmp.Close(); os.Remove(tmp.Name()) }
		n, err := io.Copy(tmp, a.stdin)
		if err != nil {
			cleanup()
			return nil, 0, "", noop, &codedError{code: "io", err: fmt.Errorf("read stdin: %w", err), exit: ExitGeneric}
		}
		if _, err := tmp.Seek(0, io.SeekStart); err != nil {
			cleanup()
			return nil, 0, "", noop, &codedError{code: "io", err: err, exit: ExitGeneric}
		}
		name := resolvedUploadName(path, nameFlag)
		return tmp, n, name, cleanup, nil
	}
	fh, err := os.Open(path)
	if err != nil {
		return nil, 0, "", noop, &codedError{code: "io", err: err, exit: ExitGeneric}
	}
	st, err := fh.Stat()
	if err != nil {
		fh.Close()
		return nil, 0, "", noop, &codedError{code: "io", err: err, exit: ExitGeneric}
	}
	if st.IsDir() {
		fh.Close()
		return nil, 0, "", noop, usagef("%s is a directory; upload a single file (tar it first)", path)
	}
	if !st.Mode().IsRegular() {
		// Pipes / devices have no reliable size: buffer them like stdin.
		defer fh.Close()
		saved := a.stdin
		a.stdin = fh
		defer func() { a.stdin = saved }()
		name := resolvedUploadName(path, nameFlag)
		return a.openSource("-", name)
	}
	name := resolvedUploadName(path, nameFlag)
	return fh, st.Size(), name, func() { fh.Close() }, nil
}

// hashAndRewind streams r through SHA-256 and seeks back to the start.
func hashAndRewind(r io.ReadSeeker) (string, error) {
	h := sha256.New()
	if _, err := io.Copy(h, r); err != nil {
		return "", err
	}
	if _, err := r.Seek(0, io.SeekStart); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func contentTypeFor(name string) string {
	ext := strings.ToLower(filepath.Ext(name))
	if ext == "" {
		return "application/octet-stream"
	}
	switch ext {
	case ".md":
		return "text/markdown; charset=utf-8"
	case ".txt", ".log":
		return "text/plain; charset=utf-8"
	case ".json":
		return "application/json"
	case ".csv":
		return "text/csv; charset=utf-8"
	}
	if ct := mime.TypeByExtension(ext); ct != "" {
		return ct
	}
	return "application/octet-stream"
}

func mustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

func (a *app) printFileLine(verb string, f api.File) {
	fmt.Fprintf(a.stdout, "%s %s %s %s expires %s\n", verb, f.ID, f.Name, fmtBytes(f.SizeBytes), fmtTime(f.ExpiresAt))
}

func (a *app) printLinkLine(l api.ShareLink) {
	max := "unlimited"
	if l.MaxDownloads != nil {
		max = fmt.Sprintf("%d", *l.MaxDownloads)
	}
	fmt.Fprintf(a.stdout, "link %s expires %s max_downloads %s\n", l.ID, fmtTime(l.ExpiresAt), max)
}
