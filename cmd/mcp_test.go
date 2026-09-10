package cmd

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
)

func TestMCPServeRejectsKeyArgument(t *testing.T) {
	t.Setenv("AISPACE_CONFIG", filepath.Join(t.TempDir(), "missing.json"))
	var stdout, stderr bytes.Buffer
	code := Run([]string{"--key", "ask_do_not_print", "mcp", "serve"}, strings.NewReader(""), &stdout, &stderr, "test")
	if code != ExitUsage {
		t.Fatalf("exit = %d, want %d", code, ExitUsage)
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q", stdout.String())
	}
	if strings.Contains(stderr.String(), "ask_do_not_print") {
		t.Fatalf("key leaked to stderr: %q", stderr.String())
	}
}

func TestMCPServeTreatsStdinEOFAsCleanShutdown(t *testing.T) {
	root := t.TempDir()
	t.Setenv("AISPACE_CONFIG", filepath.Join(root, "missing.json"))
	t.Setenv("AISPACE_ALLOWED_ROOTS", root)
	var stdout, stderr bytes.Buffer
	code := Run([]string{"mcp", "serve"}, strings.NewReader(""), &stdout, &stderr, "test")
	if code != ExitOK {
		t.Fatalf("exit = %d, stderr = %q", code, stderr.String())
	}
	if stdout.Len() != 0 || stderr.Len() != 0 {
		t.Fatalf("stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}
