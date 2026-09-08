package cmd

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTransferCommandHelpAndValidation(t *testing.T) {
	isolate(t)
	for _, args := range [][]string{{"transfer", "--help"}, {"transfer", "create", "--help"}, {"transfer", "receive", "--help"}, {"transfer", "resume", "--help"}} {
		r := run("", args...)
		if r.code != ExitOK || !strings.Contains(r.stdout, "Usage:") {
			t.Fatalf("%v: %+v", args, r)
		}
	}
	if r := run("", "transfer", "create", "file", "--sealed=false"); r.code != ExitUsage {
		t.Fatalf("unsealed create: %+v", r)
	}
	for _, args := range [][]string{
		{"transfer", "create", "file", "--sealed", "--link", "--transport", "live-only"},
		{"transfer", "create", "file", "--sealed", "--link", "--durability", "direct-first"},
		{"transfer", "create", "file", "--sealed", "--link", "--transport-privacy", "hidden"},
	} {
		if r := run("", args...); r.code != ExitUsage {
			t.Fatalf("accepted unsupported transport flags %v: %+v", args, r)
		}
	}
	if r := run("", "transfer", "receive", "not-a-token", "--yes"); r.code != ExitUsage || !strings.Contains(r.stderr, "invalid transfer reference") {
		t.Fatalf("bad reference: %+v", r)
	}
}

func TestTransferJSONSecretRefusesTerminalOutput(t *testing.T) {
	isolate(t)
	previous := outputIsTerminal
	outputIsTerminal = func(io.Writer) bool { return true }
	t.Cleanup(func() { outputIsTerminal = previous })
	result := run("", "--json", "transfer", "create", "file", "--sealed", "--link", "--include-secret")
	if result.code != ExitUsage || !strings.Contains(result.stderr, "refusing to emit") {
		t.Fatalf("terminal secret output = %+v", result)
	}
}

func TestTransferTokenFileMustBePrivate(t *testing.T) {
	isolate(t)
	path := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(path, []byte("secret\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	r := run("", "transfer", "receive", "--token-file", path, "--yes")
	if r.code != ExitUsage || !strings.Contains(r.stderr, "expected 0600") {
		t.Fatalf("result: %+v", r)
	}
}

func TestReadOneLineDoesNotConsumeFollowingPromptAnswer(t *testing.T) {
	r := strings.NewReader("token\nyes\n")
	first, err := readOneLine(r)
	if err != nil || first != "token" {
		t.Fatalf("first=%q err=%v", first, err)
	}
	second, err := readOneLine(r)
	if err != nil || second != "yes" {
		t.Fatalf("second=%q err=%v", second, err)
	}
}
