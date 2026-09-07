package cmd

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"filippo.io/age"
)

const looseIdentityWarning = "identity file"

// encryptTo writes an age file for id and returns its path.
func encryptTo(t *testing.T, dir, name, plaintext string, r age.Recipient) string {
	t.Helper()
	var buf bytes.Buffer
	w, err := age.Encrypt(&buf, r)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(w, plaintext); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// An identity file with the permissions the CLI itself writes must not be
// reported as loose. On Windows os.Stat reports 0666 for every file, so the
// unguarded check warned on every decrypt and told the user to run chmod.
func TestDecryptDoesNotWarnAboutCorrectlyPermissionedIdentity(t *testing.T) {
	isolate(t)
	dir := t.TempDir()
	id, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	enc := encryptTo(t, dir, "secret.age", "payload", id.Recipient())

	key := filepath.Join(dir, "secret.agekey")
	if err := os.WriteFile(key, []byte(id.String()+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	r := run("", "decrypt", enc, "--identity-file", key, "--output", filepath.Join(dir, "out.txt"))
	if r.code != ExitOK {
		t.Fatalf("%+v", r)
	}
	if strings.Contains(r.stderr, looseIdentityWarning) {
		t.Fatalf("warned about a mode-0600 identity file: %q", r.stderr)
	}
	got, err := os.ReadFile(filepath.Join(dir, "out.txt"))
	if err != nil || string(got) != "payload" {
		t.Fatalf("plaintext = %q err=%v", got, err)
	}
}

// Where the bits are real, a genuinely world-readable identity must still warn.
func TestDecryptWarnsAboutLooseIdentityPermissions(t *testing.T) {
	if !permBits {
		t.Skip("Unix permission bits are not implemented on this platform")
	}
	isolate(t)
	dir := t.TempDir()
	id, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	enc := encryptTo(t, dir, "secret.age", "payload", id.Recipient())

	key := filepath.Join(dir, "secret.agekey")
	if err := os.WriteFile(key, []byte(id.String()+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(key, 0o644); err != nil {
		t.Fatal(err)
	}

	r := run("", "decrypt", enc, "--identity-file", key, "--output", filepath.Join(dir, "out.txt"))
	if r.code != ExitOK {
		t.Fatalf("%+v", r)
	}
	if !strings.Contains(r.stderr, looseIdentityWarning) || !strings.Contains(r.stderr, "0644") {
		t.Fatalf("expected a warning naming the mode, got %q", r.stderr)
	}
}
