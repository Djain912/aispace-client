package cmd

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aispace-sh/aispace-client/internal/config"
)

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
