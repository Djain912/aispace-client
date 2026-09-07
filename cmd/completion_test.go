package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Replacing cobra's generated completion command must not change what the
// existing subcommands produce, since users redirect them into shell files.
func TestCompletionGeneratesEveryShell(t *testing.T) {
	isolate(t)
	for _, shell := range []string{"bash", "zsh", "fish", "powershell"} {
		t.Run(shell, func(t *testing.T) {
			r := run("", "completion", shell)
			if r.code != ExitOK {
				t.Fatalf("%+v", r)
			}
			if len(r.stdout) < 1000 || !strings.Contains(r.stdout, "aispace") {
				t.Fatalf("%s script looks wrong: %d bytes", shell, len(r.stdout))
			}
		})
	}
}

func TestCompletionRejectsUnknownShell(t *testing.T) {
	isolate(t)
	r := run("", "completion", "tcsh")
	if r.code != ExitUsage {
		t.Fatalf("exit = %d, want %d: %+v", r.code, ExitUsage, r)
	}
}

// Each shell has its own filename convention, and getting it wrong means the
// script is installed but never loaded.
func TestCompletionInstallUsesShellFileName(t *testing.T) {
	isolate(t)
	for shell, want := range map[string]string{
		"bash": "aispace",
		"zsh":  "_aispace",
		"fish": "aispace.fish",
	} {
		t.Run(shell, func(t *testing.T) {
			dir := t.TempDir()
			r := run("", "completion", "install", shell, "--dir", dir)
			if r.code != ExitOK {
				t.Fatalf("%+v", r)
			}
			path := filepath.Join(dir, want)
			info, err := os.Stat(path)
			if err != nil {
				t.Fatalf("expected %s: %v", path, err)
			}
			if info.Size() < 1000 {
				t.Fatalf("%s is only %d bytes", path, info.Size())
			}
			if !strings.Contains(r.stdout, path) {
				t.Fatalf("stdout should name the path: %q", r.stdout)
			}
		})
	}
}

// A completion file may be hand-edited, so replacing one has to be asked for.
func TestCompletionInstallRefusesToOverwrite(t *testing.T) {
	isolate(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "aispace.fish")
	if err := os.WriteFile(path, []byte("# hand written\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	r := run("", "completion", "install", "fish", "--dir", dir)
	if r.code != ExitUsage {
		t.Fatalf("exit = %d, want %d: %+v", r.code, ExitUsage, r)
	}
	if !strings.Contains(r.stderr, "--force") {
		t.Fatalf("stderr should point at --force: %q", r.stderr)
	}
	got, _ := os.ReadFile(path)
	if string(got) != "# hand written\n" {
		t.Fatalf("existing file was modified: %q", got)
	}

	r = run("", "completion", "install", "fish", "--dir", dir, "--force")
	if r.code != ExitOK {
		t.Fatalf("--force: %+v", r)
	}
	got, _ = os.ReadFile(path)
	if strings.Contains(string(got), "hand written") || len(got) < 1000 {
		t.Fatalf("--force did not replace the file (%d bytes)", len(got))
	}
}

func TestCompletionInstallJSON(t *testing.T) {
	isolate(t)
	dir := t.TempDir()
	r := run("", "completion", "install", "zsh", "--dir", dir, "--json")
	if r.code != ExitOK {
		t.Fatalf("%+v", r)
	}
	var got completionInstall
	if err := json.Unmarshal([]byte(r.stdout), &got); err != nil {
		t.Fatalf("stdout %q: %v", r.stdout, err)
	}
	if got.Shell != "zsh" || got.Path != filepath.Join(dir, "_aispace") {
		t.Fatalf("json = %+v", got)
	}
}

// Without an argument the shell comes from $SHELL, which is what a login shell
// sets. An unsupported one must say so rather than guessing.
func TestCompletionInstallDetectsShell(t *testing.T) {
	isolate(t)
	dir := t.TempDir()
	t.Setenv("SHELL", "/usr/local/bin/fish")
	r := run("", "completion", "install", "--dir", dir)
	if r.code != ExitOK {
		t.Fatalf("%+v", r)
	}
	if _, err := os.Stat(filepath.Join(dir, "aispace.fish")); err != nil {
		t.Fatalf("fish completion not installed: %v", err)
	}

	t.Setenv("SHELL", "/bin/tcsh")
	if r := run("", "completion", "install", "--dir", t.TempDir()); r.code != ExitUsage {
		t.Fatalf("tcsh should not be guessed: %+v", r)
	}

	t.Setenv("SHELL", "")
	if r := run("", "completion", "install", "--dir", t.TempDir()); r.code != ExitUsage {
		t.Fatalf("unset SHELL should be a usage error: %+v", r)
	}
}

// PowerShell loads completions from a profile, not a directory, so installing
// a file would silently do nothing.
func TestCompletionInstallRejectsPowerShell(t *testing.T) {
	isolate(t)
	r := run("", "completion", "install", "powershell", "--dir", t.TempDir())
	if r.code != ExitUsage {
		t.Fatalf("exit = %d, want %d: %+v", r.code, ExitUsage, r)
	}
	if !strings.Contains(r.stderr, "PROFILE") {
		t.Fatalf("stderr should say what to do instead: %q", r.stderr)
	}
}

// The default location follows the XDG variables the rest of the CLI honours.
func TestCompletionInstallDefaultDirFollowsXDG(t *testing.T) {
	isolate(t)
	data := t.TempDir()
	config := t.TempDir()
	t.Setenv("XDG_DATA_HOME", data)
	t.Setenv("XDG_CONFIG_HOME", config)

	if r := run("", "completion", "install", "zsh"); r.code != ExitOK {
		t.Fatalf("%+v", r)
	}
	if _, err := os.Stat(filepath.Join(data, "zsh", "site-functions", "_aispace")); err != nil {
		t.Fatalf("zsh completion not under XDG_DATA_HOME: %v", err)
	}

	if r := run("", "completion", "install", "fish"); r.code != ExitOK {
		t.Fatalf("%+v", r)
	}
	if _, err := os.Stat(filepath.Join(config, "fish", "completions", "aispace.fish")); err != nil {
		t.Fatalf("fish completion not under XDG_CONFIG_HOME: %v", err)
	}
}

// zsh needs the directory on $fpath, and that hint belongs on stderr so stdout
// stays just the installed path.
func TestCompletionInstallHintGoesToStderr(t *testing.T) {
	isolate(t)
	dir := t.TempDir()
	r := run("", "completion", "install", "zsh", "--dir", dir)
	if r.code != ExitOK {
		t.Fatalf("%+v", r)
	}
	if strings.Contains(r.stdout, "fpath") {
		t.Fatalf("hint leaked into stdout: %q", r.stdout)
	}
	if !strings.Contains(r.stderr, "fpath") {
		t.Fatalf("stderr should carry the fpath hint: %q", r.stderr)
	}
}
