package cmd

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"filippo.io/age"

	"github.com/aispace-sh/aispace-client/internal/config"
)

type failingUploadReader struct{}

func (failingUploadReader) Read([]byte) (int, error) { return 0, errors.New("source failed") }

func TestInvalidEncryptedUploadLeavesNoIdentity(t *testing.T) {
	isolate(t)
	f := newFakeServer(t)
	writeConfig(t, config.File{Key: f.key, URL: f.srv.URL})
	dir := t.TempDir()
	src := filepath.Join(dir, "secret.pdf")
	if err := os.WriteFile(src, []byte("confidential"), 0o644); err != nil {
		t.Fatal(err)
	}
	identity := filepath.Join(dir, "secret.agekey")

	r := run("", "upload", src, "--name", "bad\nname.pdf", "--encrypt", "--identity-out", identity)
	if r.code != ExitUsage {
		t.Fatalf("exit = %d, want %d: %+v", r.code, ExitUsage, r)
	}
	if _, err := os.Stat(identity); !os.IsNotExist(err) {
		t.Fatalf("identity created before header validation: err=%v", err)
	}
	if f.count() != 0 {
		t.Fatalf("no request should have been sent, got %d", f.count())
	}
}

// A rejected upload used to leave the generated identity file on disk. Nothing
// was stored, so it decrypted nothing, and it made the obvious retry of the
// same command fail with "identity file already exists" (exit 2) for arguments
// that were correct.
func TestRejectedEncryptedUploadLeavesNoStaleIdentity(t *testing.T) {
	isolate(t)
	f := newFakeServer(t)
	writeConfig(t, config.File{Key: f.key, URL: f.srv.URL})
	dir := t.TempDir()
	src := filepath.Join(dir, "secret.pdf")
	if err := os.WriteFile(src, []byte("confidential"), 0o644); err != nil {
		t.Fatal(err)
	}
	identity := filepath.Join(dir, "secret.agekey")

	f.fail = func(r *http.Request) (int, string) {
		if r.Method == http.MethodPost {
			return 402, `{"error":{"code":"quota_exceeded","message":"Account allowance exceeded"}}`
		}
		return 0, ""
	}
	r := run("", "upload", src, "--encrypt", "--identity-out", identity)
	if r.code != ExitQuota {
		t.Fatalf("exit = %d, want %d: %+v", r.code, ExitQuota, r)
	}
	if _, err := os.Stat(identity); !os.IsNotExist(err) {
		t.Fatalf("identity file survived a rejected upload: err=%v", err)
	}

	// The same command must now work once the account has room again.
	f.fail = nil
	r = run("", "upload", src, "--encrypt", "--identity-out", identity)
	if r.code != ExitOK {
		t.Fatalf("retry after freeing quota = %+v, want success", r)
	}
	if _, err := os.Stat(identity); err != nil {
		t.Fatalf("identity file missing after a successful upload: %v", err)
	}
}

// When the request fails without a response the file may have been stored, so
// the identity is the only way to read it and must be kept.
func TestUploadKeepsIdentityWhenOutcomeIsUnknown(t *testing.T) {
	isolate(t)
	f := newFakeServer(t)
	writeConfig(t, config.File{Key: f.key, URL: f.srv.URL})
	dir := t.TempDir()
	src := filepath.Join(dir, "secret.pdf")
	if err := os.WriteFile(src, []byte("confidential"), 0o644); err != nil {
		t.Fatal(err)
	}
	identity := filepath.Join(dir, "secret.agekey")

	// Close the server so the request fails at the transport, with no status.
	f.srv.Close()
	r := run("", "upload", src, "--encrypt", "--identity-out", identity)
	if r.code != ExitGeneric {
		t.Fatalf("exit = %d, want %d: %+v", r.code, ExitGeneric, r)
	}
	if _, err := os.Stat(identity); err != nil {
		t.Fatalf("identity file was removed after an unknown outcome: %v", err)
	}
	if !strings.Contains(r.stderr, "kept identity file") {
		t.Fatalf("stderr should explain why the file was kept: %q", r.stderr)
	}
}

func TestUploadKeepsGeneratedRecoveryIdentityWhenOutcomeIsUnknown(t *testing.T) {
	isolate(t)
	f := newFakeServer(t)
	writeConfig(t, config.File{Key: f.key, URL: f.srv.URL})
	dir := t.TempDir()
	src := filepath.Join(dir, "secret.pdf")
	if err := os.WriteFile(src, []byte("confidential"), 0o644); err != nil {
		t.Fatal(err)
	}

	f.srv.Close()
	r := run("", "upload", src, "--encrypt")
	if r.code != ExitGeneric {
		t.Fatalf("exit = %d, want %d: %+v", r.code, ExitGeneric, r)
	}
	const prefix = "warning: kept recovery identity file "
	start := strings.Index(r.stderr, prefix)
	if start < 0 {
		t.Fatalf("stderr should name a recovery identity: %q", r.stderr)
	}
	path := strings.SplitN(r.stderr[start+len(prefix):], ";", 2)[0]
	t.Cleanup(func() { _ = os.Remove(path) })
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read recovery identity: %v", err)
	}
	if _, err := age.ParseX25519Identity(strings.TrimSpace(string(raw))); err != nil {
		t.Fatalf("recovery file does not contain a usable identity: %v", err)
	}
}

func TestGeneratedRecoveryIdentityIsTemporaryOnSuccess(t *testing.T) {
	e, err := encryptForUpload(strings.NewReader("secret"), "secret.txt", "", "")
	if err != nil {
		t.Fatal(err)
	}
	path := e.recoveryFile
	if path == "" {
		t.Fatal("generated upload has no recovery identity")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("recovery identity was not created: %v", err)
	}
	e.cleanup()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("recovery identity survived cleanup: %v", err)
	}
}

func TestLocalEncryptionFailureRemovesIdentityFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secret.agekey")
	_, err := encryptForUpload(failingUploadReader{}, "secret.txt", "", path)
	if err == nil {
		t.Fatal("expected encryption to fail")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("identity file survived local encryption failure: %v", err)
	}
}

func TestUploadKeepsIdentityAfterServerFailure(t *testing.T) {
	for _, status := range []int{http.StatusInternalServerError, http.StatusBadGateway, http.StatusGatewayTimeout} {
		t.Run(fmt.Sprintf("HTTP %d", status), func(t *testing.T) {
			isolate(t)
			f := newFakeServer(t)
			writeConfig(t, config.File{Key: f.key, URL: f.srv.URL})
			dir := t.TempDir()
			src := filepath.Join(dir, "secret.pdf")
			if err := os.WriteFile(src, []byte("confidential"), 0o644); err != nil {
				t.Fatal(err)
			}
			identity := filepath.Join(dir, "secret.agekey")

			f.fail = func(r *http.Request) (int, string) {
				if r.Method == http.MethodPost {
					return status, `{"error":{"code":"internal","message":"server failed"}}`
				}
				return 0, ""
			}
			r := run("", "upload", src, "--encrypt", "--identity-out", identity)
			if r.code != ExitGeneric {
				t.Fatalf("exit = %d, want %d: %+v", r.code, ExitGeneric, r)
			}
			if _, err := os.Stat(identity); err != nil {
				t.Fatalf("identity removed after uncertain server failure: %v", err)
			}
			if !strings.Contains(r.stderr, "kept identity file") {
				t.Fatalf("stderr should explain why the file was kept: %q", r.stderr)
			}
		})
	}
}

// A link failure happens after the file is stored, so the identity must survive.
func TestLinkFailureKeepsIdentity(t *testing.T) {
	isolate(t)
	f := newFakeServer(t)
	writeConfig(t, config.File{Key: f.key, URL: f.srv.URL})
	dir := t.TempDir()
	src := filepath.Join(dir, "secret.pdf")
	if err := os.WriteFile(src, []byte("confidential"), 0o644); err != nil {
		t.Fatal(err)
	}
	identity := filepath.Join(dir, "secret.agekey")

	f.fail = func(r *http.Request) (int, string) {
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/links") {
			return 402, `{"error":{"code":"payment_required","message":"Links require Pro"}}`
		}
		return 0, ""
	}
	r := run("", "upload", src, "--encrypt", "--identity-out", identity, "--link")
	if r.code != ExitQuota {
		t.Fatalf("exit = %d, want %d: %+v", r.code, ExitQuota, r)
	}
	if _, err := os.Stat(identity); err != nil {
		t.Fatalf("identity removed although the file was stored: %v", err)
	}
}
