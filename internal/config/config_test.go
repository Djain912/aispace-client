package config

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// permBits reports whether the platform implements Unix permission bits.
// Windows does not: os.Chmod there only toggles the read-only attribute, so
// Save cannot produce (and Load cannot observe) mode 0600. Load already skips
// its permission warning on Windows for the same reason.
var permBits = runtime.GOOS != "windows"

// setHome redirects config lookups into a temporary directory and clears the
// environment overrides.
//
// It then asserts the redirect actually took effect, because every caller
// writes to Path(): if the redirect silently failed, the tests below would
// overwrite the developer's real config file and destroy a saved API key.
func setHome(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	// os.UserHomeDir reads USERPROFILE on Windows and HOME everywhere else.
	// Set both so the redirect holds on every platform.
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("AISPACE_CONFIG", "")
	t.Setenv(EnvKey, "")
	t.Setenv(EnvURL, "")
	p, err := Path()
	if err != nil {
		t.Fatalf("Path() while isolating the test: %v", err)
	}
	if !strings.HasPrefix(p, dir) {
		t.Fatalf("config path %s escaped the test home %s; refusing to run so the real config is not overwritten", p, dir)
	}
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
	if permBits && st.Mode().Perm() != 0o600 {
		t.Fatalf("perm = %04o, want 0600", st.Mode().Perm())
	}
	dst, _ := os.Stat(filepath.Dir(p))
	if permBits && dst.Mode().Perm() != 0o700 {
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
	if permBits && st.Mode().Perm() != 0o600 {
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
	if !permBits {
		t.Skip("Unix permission bits are not implemented on this platform")
	}
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
	if !permBits {
		t.Skip("Unix permission bits are not implemented on this platform")
	}
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
