package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func setHome(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("AISPACE_CONFIG", "")
	t.Setenv(EnvKey, "")
	t.Setenv(EnvURL, "")
	return dir
}

func TestPathDefaultsAndXDG(t *testing.T) {
	home := setHome(t)
	p, err := Path()
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(home, ".config", "aispace", "config.json"); p != want {
		t.Fatalf("Path() = %s, want %s", p, want)
	}
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	p, _ = Path()
	if want := filepath.Join(xdg, "aispace", "config.json"); p != want {
		t.Fatalf("Path() with XDG = %s, want %s", p, want)
	}
}

func TestSaveCreates0600AndRoundTrips(t *testing.T) {
	setHome(t)
	p, _ := Path()
	if err := Save(p, File{Key: "ask_abc", URL: "https://example.test"}); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("perm = %04o, want 0600", st.Mode().Perm())
	}
	dst, _ := os.Stat(filepath.Dir(p))
	if dst.Mode().Perm() != 0o700 {
		t.Fatalf("dir perm = %04o, want 0700", dst.Mode().Perm())
	}
	f, warns, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(warns) != 0 {
		t.Fatalf("unexpected warnings: %v", warns)
	}
	if f.Key != "ask_abc" || f.URL != "https://example.test" {
		t.Fatalf("round trip mismatch: %+v", f)
	}
	// Overwrite keeps 0600.
	if err := Save(p, File{Key: "ask_def"}); err != nil {
		t.Fatal(err)
	}
	st, _ = os.Stat(p)
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("perm after overwrite = %04o", st.Mode().Perm())
	}
	entries, _ := os.ReadDir(filepath.Dir(p))
	if len(entries) != 1 {
		t.Fatalf("temp file left behind: %v", entries)
	}
}

func TestLoadMissingIsEmpty(t *testing.T) {
	setHome(t)
	p, _ := Path()
	f, warns, err := Load(p)
	if err != nil || f != (File{}) || warns != nil {
		t.Fatalf("Load missing = %+v, %v, %v", f, warns, err)
	}
}

func TestLoadWarnsOnLoosePerms(t *testing.T) {
	setHome(t)
	p, _ := Path()
	if err := Save(p, File{Key: "ask_abc"}); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, 0o644); err != nil {
		t.Fatal(err)
	}
	_, warns, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(warns) != 1 || !strings.Contains(warns[0], "0644") || !strings.Contains(warns[0], "0600") {
		t.Fatalf("warnings = %v", warns)
	}
}

func TestLoadBadJSON(t *testing.T) {
	setHome(t)
	p, _ := Path()
	_ = os.MkdirAll(filepath.Dir(p), 0o700)
	_ = os.WriteFile(p, []byte("{nope"), 0o600)
	if _, _, err := Load(p); err == nil {
		t.Fatal("expected parse error")
	}
}

func TestResolvePrecedence(t *testing.T) {
	setHome(t)
	p, _ := Path()

	// Nothing anywhere: default URL, empty key.
	r, err := Resolve("", "")
	if err != nil {
		t.Fatal(err)
	}
	if r.Key != "" || r.URL != DefaultURL {
		t.Fatalf("defaults = %+v", r)
	}

	// File only.
	if err := Save(p, File{Key: "ask_file", URL: "https://file.test/"}); err != nil {
		t.Fatal(err)
	}
	r, _ = Resolve("", "")
	if r.Key != "ask_file" || r.URL != "https://file.test" {
		t.Fatalf("file = %+v (trailing slash should be trimmed)", r)
	}

	// Env beats file.
	t.Setenv(EnvKey, "ask_env")
	t.Setenv(EnvURL, "https://env.test")
	r, _ = Resolve("", "")
	if r.Key != "ask_env" || r.URL != "https://env.test" {
		t.Fatalf("env = %+v", r)
	}

	// Flag beats env.
	r, _ = Resolve("ask_flag", "https://flag.test")
	if r.Key != "ask_flag" || r.URL != "https://flag.test" {
		t.Fatalf("flag = %+v", r)
	}

	// Partial override: flag key only, URL still from env.
	r, _ = Resolve("ask_flag", "")
	if r.Key != "ask_flag" || r.URL != "https://env.test" {
		t.Fatalf("partial = %+v", r)
	}
}

func TestResolveSurfacesPermWarning(t *testing.T) {
	setHome(t)
	p, _ := Path()
	_ = Save(p, File{Key: "ask_file"})
	_ = os.Chmod(p, 0o666)
	r, err := Resolve("", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Warnings) != 1 {
		t.Fatalf("warnings = %v", r.Warnings)
	}
}

func TestAispaceConfigOverride(t *testing.T) {
	setHome(t)
	custom := filepath.Join(t.TempDir(), "c.json")
	t.Setenv("AISPACE_CONFIG", custom)
	p, _ := Path()
	if p != custom {
		t.Fatalf("Path() = %s, want %s", p, custom)
	}
}
