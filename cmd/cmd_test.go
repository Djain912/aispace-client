package cmd

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"testing/iotest"
	"time"

	"filippo.io/age"

	"github.com/aispace-sh/aispace-client/internal/config"
)

// fakeServer is a minimal in-memory aispace /v1 implementation.
type fakeServer struct {
	mu       sync.Mutex
	srv      *httptest.Server
	key      string
	requests []*http.Request
	bodies   [][]byte
	// scripted overrides
	status429Once map[string]bool
	fail          func(r *http.Request) (int, string)
}

func newFakeServer(t *testing.T) *fakeServer {
	t.Helper()
	f := &fakeServer{key: "ask_validkey0000000000000000000000", status429Once: map[string]bool{}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeServer) handle(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	f.requests = append(f.requests, r)
	f.bodies = append(f.bodies, body)
	f.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	if r.Header.Get("Authorization") != "Bearer "+f.key {
		w.WriteHeader(401)
		_, _ = w.Write([]byte(`{"error":{"code":"invalid_key","message":"Invalid key"}}`))
		return
	}
	if f.fail != nil {
		if st, b := f.fail(r); st != 0 {
			if st == 429 {
				w.Header().Set("Retry-After", "0")
			}
			w.WriteHeader(st)
			_, _ = w.Write([]byte(b))
			return
		}
	}
	routeKey := r.Method + " " + r.URL.Path
	f.mu.Lock()
	once := f.status429Once[routeKey]
	if once {
		delete(f.status429Once, routeKey)
	}
	f.mu.Unlock()
	if once {
		w.Header().Set("Retry-After", "0")
		w.WriteHeader(429)
		_, _ = w.Write([]byte(`{"error":{"code":"rate_limited","message":"slow down"}}`))
		return
	}

	switch {
	case r.Method == "GET" && r.URL.Path == "/v1/whoami":
		_, _ = w.Write([]byte(`{"key":{"id":"k1","name":"research-bot","prefix":"ask_validkey","budget_bytes":52428800,"used_bytes":1048576,"created_at":1,"last_used_at":null,"revoked_at":null},"user":{"email":"luigi@example.com"}}`))
	case r.Method == "POST" && r.URL.Path == "/v1/files":
		if r.ContentLength < 0 {
			w.WriteHeader(400)
			_, _ = w.Write([]byte(`{"error":{"code":"missing_content_length","message":"Content-Length required"}}`))
			return
		}
		resp := map[string]any{
			"id": "01FILE", "name": r.Header.Get("X-File-Name"), "content_type": r.Header.Get("Content-Type"),
			"size_bytes": len(body), "sha256": r.Header.Get("X-SHA256"), "enc_alg": nullableString(r.Header.Get("X-Aispace-Encryption")), "visibility": uploadVisibility(r), "created_at": 1757000000, "expires_at": 1757604800,
		}
		w.WriteHeader(201)
		_ = json.NewEncoder(w).Encode(resp)
	case r.Method == "GET" && r.URL.Path == "/v1/files":
		switch r.URL.Query().Get("cursor") {
		case "":
			_, _ = w.Write([]byte(`{"files":[{"id":"01A","name":"a.txt","content_type":"text/plain","size_bytes":5,"created_at":1,"expires_at":1757604800}],"next_cursor":"p2"}`))
		case "p2":
			_, _ = w.Write([]byte(`{"files":[{"id":"01B","name":"b.pdf","content_type":"application/pdf","size_bytes":2048,"created_at":1,"expires_at":1757604800}],"next_cursor":null}`))
		}
	case r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/links"):
		_, _ = w.Write([]byte(`{"links":[{"id":"01L1","file_id":"01A","expires_at":1757003600,"max_downloads":3,"download_count":1,"created_at":1757000000,"revoked_at":null},{"id":"01L2","file_id":"01A","expires_at":1757003600,"max_downloads":null,"download_count":0,"created_at":1757000000,"revoked_at":1757000100}]}`))
	case r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/content"):
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("shared contents"))
	case r.Method == "GET" && strings.HasPrefix(r.URL.Path, "/v1/files/"):
		_, _ = w.Write([]byte(`{"id":"01A","name":"a.txt","content_type":"text/plain","size_bytes":5,"sha256":"deadbeef","visibility":"account","created_at":1757000000,"expires_at":1757604800}`))
	case r.Method == "DELETE" && strings.HasPrefix(r.URL.Path, "/v1/files/"):
		w.WriteHeader(204)
	case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/links"):
		var in map[string]int64
		_ = json.Unmarshal(body, &in)
		resp := map[string]any{"id": "01LINK", "file_id": strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/v1/files/"), "/links"),
			"url": "https://aispace.test/d/tok123", "expires_at": 1757003600, "download_count": 0, "created_at": 1757000000, "revoked_at": nil}
		if md, ok := in["max_downloads"]; ok {
			resp["max_downloads"] = md
		} else {
			resp["max_downloads"] = nil
		}
		w.WriteHeader(201)
		_ = json.NewEncoder(w).Encode(resp)
	case r.Method == "DELETE" && strings.HasPrefix(r.URL.Path, "/v1/links/"):
		w.WriteHeader(204)
	case r.Method == "GET" && r.URL.Path == "/v1/quota":
		_, _ = w.Write([]byte(`{"key":{"budget_bytes":52428800,"used_bytes":1048576,"remaining_bytes":51380224},"account":{"allowance_bytes":104857600,"used_bytes":3145728,"remaining_bytes":101711872,"plan":"free","extra_blocks":0},"month":{"uploads_used":12,"uploads_limit":100,"downloads_used":340,"downloads_limit":1000,"period_end":1759276800},"limits":{"max_file_bytes":26214400,"max_file_ttl_seconds":2592000,"max_link_ttl_seconds":604800,"uploads_per_hour":60,"uploads_per_day":500,"requests_per_minute":300},"rate":{"uploads_hour_remaining":58,"uploads_day_remaining":490,"requests_minute_remaining":299}}`))
	default:
		w.WriteHeader(404)
		_, _ = w.Write([]byte(`{"error":{"code":"not_found","message":"no route"}}`))
	}
}

func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func uploadVisibility(r *http.Request) string {
	if value := r.Header.Get("X-File-Visibility"); value != "" {
		return value
	}
	return "account"
}

func (f *fakeServer) last() (*http.Request, []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.requests[len(f.requests)-1], f.bodies[len(f.bodies)-1]
}

func (f *fakeServer) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.requests)
}

// permBits reports whether the platform implements Unix permission bits.
// Windows does not: os.Chmod there only toggles the read-only attribute, so the
// 0600 files this package writes cannot be observed as 0600.
var permBits = runtime.GOOS != "windows"

// isolate points HOME/XDG at a temp dir and clears env overrides. It verifies
// the redirect took effect so a test can never write to the real config file.
func isolate(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	// os.UserHomeDir reads USERPROFILE on Windows and HOME everywhere else.
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "xdg"))
	t.Setenv("AISPACE_CONFIG", "")
	t.Setenv(config.EnvKey, "")
	t.Setenv(config.EnvURL, "")
	t.Setenv(identityEnv, "")
	p, err := config.Path()
	if err != nil {
		t.Fatalf("config.Path() while isolating the test: %v", err)
	}
	if !strings.HasPrefix(p, dir) {
		t.Fatalf("config path %s escaped the test home %s; refusing to run so the real config is not overwritten", p, dir)
	}
	old := sleep
	sleep = func(time.Duration) {}
	t.Cleanup(func() { sleep = old })
	return dir
}

type result struct {
	code   int
	stdout string
	stderr string
}

func run(stdin string, args ...string) result {
	var out, errb bytes.Buffer
	code := Run(args, strings.NewReader(stdin), &out, &errb, "1.2.3")
	return result{code, out.String(), errb.String()}
}

func writeConfig(t *testing.T, f config.File) string {
	t.Helper()
	p, err := config.Path()
	if err != nil {
		t.Fatal(err)
	}
	if err := config.Save(p, f); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestVersion(t *testing.T) {
	isolate(t)
	r := run("", "version")
	if r.code != 0 || !strings.HasPrefix(r.stdout, "aispace 1.2.3 (") {
		t.Fatalf("%+v", r)
	}
	r = run("", "version", "--json")
	var v map[string]string
	if err := json.Unmarshal([]byte(r.stdout), &v); err != nil || v["version"] != "1.2.3" || !strings.HasPrefix(v["user_agent"], "aispace-cli/1.2.3 (") {
		t.Fatalf("%+v %v", r, err)
	}
}

func TestKeygen(t *testing.T) {
	isolate(t)
	r := run("", "keygen", "--json")
	if r.code != 0 || r.stderr != "" {
		t.Fatalf("%+v", r)
	}
	var out keygenOutput
	if err := json.Unmarshal([]byte(r.stdout), &out); err != nil {
		t.Fatal(err)
	}
	identity, err := age.ParseX25519Identity(out.Identity)
	if err != nil {
		t.Fatalf("invalid generated identity: %v", err)
	}
	if out.Algorithm != clientEncryptionAlgorithm || out.Recipient != identity.Recipient().String() || out.IdentityFile != "" {
		t.Fatalf("unexpected output: %+v", out)
	}
}

func TestKeygenIdentityFile(t *testing.T) {
	isolate(t)
	path := filepath.Join(t.TempDir(), "receiver.agekey")
	r := run("", "keygen", "--identity-out", path, "--json")
	if r.code != 0 || r.stderr != "" {
		t.Fatalf("%+v", r)
	}
	var out keygenOutput
	if err := json.Unmarshal([]byte(r.stdout), &out); err != nil {
		t.Fatal(err)
	}
	if out.Identity != "" || out.IdentityFile != path {
		t.Fatalf("secret leaked or path missing: %+v", out)
	}
	info, err := os.Stat(path)
	if err != nil || (permBits && info.Mode().Perm() != 0o600) {
		t.Fatalf("identity file: info=%v err=%v", info, err)
	}
	r = run("", "keygen", "--identity-out", path)
	if r.code != ExitUsage || !strings.Contains(r.stderr, "identity file already exists") {
		t.Fatalf("existing identity was overwritten: %+v", r)
	}
	stored, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	identity, err := age.ParseX25519Identity(strings.TrimSpace(string(stored)))
	if err != nil || identity.Recipient().String() != out.Recipient {
		t.Fatalf("stored identity does not match recipient: %v", err)
	}
}

func TestUsageErrors(t *testing.T) {
	isolate(t)
	cases := [][]string{
		{"bogus"},
		{"ls", "--nope"},
		{"upload"},
		{"upload", "a", "b"},
		{"link"},
		{"rm"},
		{"revoke"},
		{"quota", "extra"},
		{"login"},
		{"login", "--key", "notask_x"},
		{"upload", "x", "--key", "ask_x", "--expires", "bogus"},
		{"upload", "x", "--key", "ask_x", "--link-expires", "1h"},
		{"upload", "x", "--key", "ask_x", "--max-downloads", "-1", "--link"},
		{"link", "f", "--key", "ask_x", "--expires", "1w"},
		{"ls", "--key", "ask_x", "--url", "ftp://x"},
	}
	for _, args := range cases {
		r := run("", args...)
		if r.code != ExitUsage {
			t.Errorf("%v: code %d, stderr %q", args, r.code, r.stderr)
		}
		if !strings.HasPrefix(r.stderr, "error: ") || !strings.HasSuffix(strings.TrimSpace(r.stderr), "(usage)") {
			t.Errorf("%v: stderr format %q", args, r.stderr)
		}
		if r.stdout != "" {
			t.Errorf("%v: unexpected stdout %q", args, r.stdout)
		}
	}
}

func TestRootHelpExitsZero(t *testing.T) {
	isolate(t)
	r := run("")
	if r.code != 0 || !strings.Contains(r.stdout, "Available Commands") {
		t.Fatalf("%+v", r)
	}
}

func TestNoKeyIsAuthExit(t *testing.T) {
	isolate(t)
	r := run("", "ls")
	if r.code != ExitAuth || !strings.Contains(r.stderr, "(unauthenticated)") {
		t.Fatalf("%+v", r)
	}
	r = run("", "ls", "--json")
	var e struct {
		Error struct {
			Code     string `json:"code"`
			ExitCode int    `json:"exit_code"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(r.stderr), &e); err != nil || e.Error.Code != "unauthenticated" || e.Error.ExitCode != 3 {
		t.Fatalf("json error: %+v %v", r, err)
	}
}

func TestServerURLRejectsRemotePlainHTTP(t *testing.T) {
	isolate(t)
	key := "ask_validkey0000000000000000000000"
	for _, raw := range []string{
		"http://example.com",
		"https://user:pass@example.com",
		"https://example.com/api",
		"https://example.com?token=leak",
	} {
		r := run("", "whoami", "--key", key, "--url", raw)
		if r.code != ExitUsage {
			t.Errorf("%s: code %d, stderr %q", raw, r.code, r.stderr)
		}
	}
	for _, raw := range []string{"http://localhost:8787", "http://127.0.0.1:8787", "http://[::1]:8787", "https://example.com"} {
		if err := validateServerURL(raw); err != nil {
			t.Errorf("%s: unexpected error: %v", raw, err)
		}
	}
}

func TestLoginWritesConfigAndValidates(t *testing.T) {
	dir := isolate(t)
	f := newFakeServer(t)
	r := run("", "login", "--key", f.key, "--url", f.srv.URL)
	if r.code != 0 {
		t.Fatalf("%+v", r)
	}
	if !strings.Contains(r.stdout, `logged in as key "research-bot" (ask_validkey) for luigi@example.com`) {
		t.Fatalf("stdout %q", r.stdout)
	}
	if strings.Contains(r.stdout+r.stderr, f.key) {
		t.Fatal("key echoed")
	}
	p := filepath.Join(dir, "xdg", "aispace", "config.json")
	st, err := os.Stat(p)
	if err != nil {
		t.Fatalf("config not written at XDG path: %v", err)
	}
	if permBits && st.Mode().Perm() != 0o600 {
		t.Fatalf("perm %04o", st.Mode().Perm())
	}
	got, _, _ := config.Load(p)
	if got.Key != f.key || got.URL != f.srv.URL {
		t.Fatalf("config %+v", got)
	}
	req, _ := f.last()
	if req.URL.Path != "/v1/whoami" || !strings.HasPrefix(req.Header.Get("User-Agent"), "aispace-cli/1.2.3 (") {
		t.Fatalf("req %s UA %q", req.URL.Path, req.Header.Get("User-Agent"))
	}

	// Subsequent commands pick up the saved config.
	r = run("", "whoami")
	if r.code != 0 || !strings.Contains(r.stdout, "research-bot (ask_validkey) luigi@example.com") {
		t.Fatalf("%+v", r)
	}
}

func TestLoginFromEnvKey(t *testing.T) {
	isolate(t)
	f := newFakeServer(t)
	t.Setenv(config.EnvKey, f.key)
	t.Setenv(config.EnvURL, f.srv.URL)
	r := run("", "login")
	if r.code != 0 {
		t.Fatalf("%+v", r)
	}
	r = run("", "login", "--json")
	if r.code != 0 || !strings.HasPrefix(r.stdout, `{"key":`) {
		t.Fatalf("%+v", r)
	}
}

func TestLoginInvalidKey(t *testing.T) {
	dir := isolate(t)
	f := newFakeServer(t)
	r := run("", "login", "--key", "ask_wrong", "--url", f.srv.URL)
	if r.code != ExitAuth || strings.TrimSpace(r.stderr) != "error: Invalid key (invalid_key)" {
		t.Fatalf("%+v", r)
	}
	if _, err := os.Stat(filepath.Join(dir, "xdg", "aispace", "config.json")); !os.IsNotExist(err) {
		t.Fatal("config should not be written on failed login")
	}
}

func TestPrecedenceFlagEnvFile(t *testing.T) {
	isolate(t)
	f := newFakeServer(t)
	other := newFakeServer(t)
	other.key = "ask_otherkey"
	writeConfig(t, config.File{Key: "ask_filekey", URL: "http://127.0.0.1:1"})

	// File only: wrong URL -> network error exit 1.
	r := run("", "quota")
	if r.code != ExitGeneric || !strings.Contains(r.stderr, "(network)") {
		t.Fatalf("file only: %+v", r)
	}
	// Env beats file.
	t.Setenv(config.EnvKey, f.key)
	t.Setenv(config.EnvURL, f.srv.URL)
	r = run("", "quota")
	if r.code != 0 {
		t.Fatalf("env: %+v", r)
	}
	// Flag beats env.
	r = run("", "quota", "--key", other.key, "--url", other.srv.URL)
	if r.code != 0 || other.count() != 1 {
		t.Fatalf("flag: %+v (other count %d)", r, other.count())
	}
	// Wrong key via flag -> 401 -> exit 3.
	r = run("", "quota", "--key", "ask_nope", "--url", f.srv.URL)
	if r.code != ExitAuth {
		t.Fatalf("bad key: %+v", r)
	}
}

func TestConfigPermWarning(t *testing.T) {
	if !permBits {
		t.Skip("Unix permission bits are not implemented on this platform")
	}
	isolate(t)
	f := newFakeServer(t)
	p := writeConfig(t, config.File{Key: f.key, URL: f.srv.URL})
	_ = os.Chmod(p, 0o644)
	r := run("", "quota")
	if r.code != 0 || !strings.Contains(r.stderr, "warning: config file") || !strings.Contains(r.stderr, "0600") {
		t.Fatalf("%+v", r)
	}
}

func TestUploadFile(t *testing.T) {
	isolate(t)
	f := newFakeServer(t)
	writeConfig(t, config.File{Key: f.key, URL: f.srv.URL})
	path := filepath.Join(t.TempDir(), "report.pdf")
	content := []byte("%PDF-1.4 fake")
	_ = os.WriteFile(path, content, 0o644)

	r := run("", "upload", path, "--expires", "1d", "--sha256")
	if r.code != 0 {
		t.Fatalf("%+v", r)
	}
	if !strings.HasPrefix(r.stdout, "uploaded 01FILE report.pdf 13 B expires 2025-09-11T") {
		t.Fatalf("stdout %q", r.stdout)
	}
	req, body := f.last()
	sum := sha256.Sum256(content)
	if req.Header.Get("X-File-Name") != "report.pdf" ||
		req.Header.Get("X-Expires-In") != "86400" ||
		req.Header.Get("X-SHA256") != hex.EncodeToString(sum[:]) ||
		req.Header.Get("Content-Type") != "application/pdf" ||
		req.ContentLength != int64(len(content)) ||
		!bytes.Equal(body, content) {
		t.Fatalf("headers %v len %d body %q", req.Header, req.ContentLength, body)
	}

	// --name override, no expires header, no sha header.
	r = run("", "upload", path, "--name", "renamed.bin", "--json")
	if r.code != 0 {
		t.Fatalf("%+v", r)
	}
	req, _ = f.last()
	if req.Header.Get("X-File-Name") != "renamed.bin" || req.Header.Get("X-Expires-In") != "" || req.Header.Get("X-SHA256") != "" || req.Header.Get("Content-Type") != "application/octet-stream" {
		t.Fatalf("headers %v", req.Header)
	}
	var file map[string]any
	if err := json.Unmarshal([]byte(r.stdout), &file); err != nil || file["id"] != "01FILE" || file["name"] != "renamed.bin" {
		t.Fatalf("json %q %v", r.stdout, err)
	}
}

func TestEncryptedUploadAndLocalDecrypt(t *testing.T) {
	isolate(t)
	f := newFakeServer(t)
	writeConfig(t, config.File{Key: f.key, URL: f.srv.URL})
	dir := t.TempDir()
	plainPath := filepath.Join(dir, "private.txt")
	identityPath := filepath.Join(dir, "private.agekey")
	if err := os.WriteFile(plainPath, []byte("confidential payload"), 0o644); err != nil {
		t.Fatal(err)
	}

	r := run("", "upload", plainPath, "--encrypt", "--identity-out", identityPath, "--json")
	if r.code != 0 {
		t.Fatalf("%+v", r)
	}
	var out struct {
		File struct {
			Name   string `json:"name"`
			EncAlg string `json:"enc_alg"`
		} `json:"file"`
		Encryption struct {
			Algorithm    string `json:"algorithm"`
			Recipient    string `json:"recipient"`
			Identity     string `json:"identity"`
			IdentityFile string `json:"identity_file"`
		} `json:"encryption"`
	}
	if err := json.Unmarshal([]byte(r.stdout), &out); err != nil {
		t.Fatal(err)
	}
	if out.File.Name != "private.txt.age" || out.File.EncAlg != clientEncryptionAlgorithm || out.Encryption.Algorithm != clientEncryptionAlgorithm {
		t.Fatalf("unexpected encrypted response: %+v", out)
	}
	if !strings.HasPrefix(out.Encryption.Recipient, "age1") || out.Encryption.Identity != "" || out.Encryption.IdentityFile != identityPath {
		t.Fatalf("unexpected key metadata: %+v", out.Encryption)
	}
	identityInfo, err := os.Stat(identityPath)
	if err != nil {
		t.Fatal(err)
	}
	if permBits && identityInfo.Mode().Perm() != 0o600 {
		t.Fatalf("identity mode = %o", identityInfo.Mode().Perm())
	}
	req, ciphertext := f.last()
	if req.Header.Get("X-Aispace-Encryption") != clientEncryptionAlgorithm || req.Header.Get("Content-Type") != encryptedContentType {
		t.Fatalf("headers: %v", req.Header)
	}
	if req.Header.Get("X-SHA256") == "" || bytes.Contains(ciphertext, []byte("confidential payload")) {
		t.Fatalf("ciphertext/hash invalid: headers=%v body=%q", req.Header, ciphertext)
	}

	decryptedPath := filepath.Join(dir, "download.txt")
	download := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", encryptedContentType)
		_, _ = w.Write(ciphertext)
	}))
	defer download.Close()
	r = run("", "decrypt", download.URL, "--identity-file", identityPath, "--output", decryptedPath)
	if r.code != 0 || !strings.Contains(r.stdout, "decrypted ") {
		t.Fatalf("%+v", r)
	}
	got, err := os.ReadFile(decryptedPath)
	if err != nil || string(got) != "confidential payload" {
		t.Fatalf("plaintext %q err=%v", got, err)
	}
	decryptedInfo, err := os.Stat(decryptedPath)
	if err != nil || (permBits && decryptedInfo.Mode().Perm() != 0o600) {
		t.Fatalf("decrypted mode/info = %v err=%v", decryptedInfo, err)
	}
}

func TestEncryptedUploadEmitsIdentityAndRejectsTampering(t *testing.T) {
	isolate(t)
	f := newFakeServer(t)
	writeConfig(t, config.File{Key: f.key, URL: f.srv.URL})
	r := run("secret", "upload", "-", "--name", "message.txt", "--encrypt", "--json")
	if r.code != 0 {
		t.Fatalf("%+v", r)
	}
	var out struct {
		Encryption struct {
			Identity string `json:"identity"`
		} `json:"encryption"`
	}
	if err := json.Unmarshal([]byte(r.stdout), &out); err != nil || !strings.HasPrefix(out.Encryption.Identity, "AGE-SECRET-KEY-") {
		t.Fatalf("identity missing from %q: %v", r.stdout, err)
	}
	_, ciphertext := f.last()
	t.Setenv(identityEnv, out.Encryption.Identity)
	r = run(string(ciphertext), "decrypt", "-", "--output", "-")
	if r.code != 0 || r.stdout != "secret" {
		t.Fatalf("%+v", r)
	}

	corrupt := append([]byte(nil), ciphertext...)
	corrupt[len(corrupt)-1] ^= 1
	output := filepath.Join(t.TempDir(), "must-not-exist.txt")
	r = run(string(corrupt), "decrypt", "-", "--output", output)
	if r.code == 0 || !strings.Contains(r.stderr, "(decryption)") {
		t.Fatalf("tampered ciphertext accepted: %+v", r)
	}
	if _, err := os.Stat(output); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("partial plaintext was not removed: %v", err)
	}
}

func TestEncryptionFlagValidation(t *testing.T) {
	isolate(t)
	for _, args := range [][]string{
		{"upload", "-", "--recipient", "age1invalid"},
		{"upload", "-", "--identity-out", "key.txt"},
		{"upload", "-", "--encrypt", "--recipient", "age1invalid", "--identity-out", "key.txt"},
		{"decrypt", "-", "--output", "-", "--json"},
	} {
		r := run("x", args...)
		if r.code != ExitUsage {
			t.Fatalf("args %v: %+v", args, r)
		}
	}
}

func TestUploadStdinWithLink(t *testing.T) {
	isolate(t)
	f := newFakeServer(t)
	writeConfig(t, config.File{Key: f.key, URL: f.srv.URL})

	r := run("hi\n", "upload", "-", "--name", "note.txt", "--link", "--link-expires", "2h", "--max-downloads", "3")
	if r.code != 0 {
		t.Fatalf("%+v", r)
	}
	lines := strings.Split(strings.TrimSpace(r.stdout), "\n")
	if len(lines) != 3 {
		t.Fatalf("expected 3 lines, got %q", r.stdout)
	}
	if !strings.HasPrefix(lines[0], "uploaded 01FILE note.txt 3 B") {
		t.Fatalf("line0 %q", lines[0])
	}
	if !strings.HasPrefix(lines[1], "link 01LINK expires ") || !strings.HasSuffix(lines[1], "max_downloads 3") {
		t.Fatalf("line1 %q", lines[1])
	}
	if lines[2] != "https://aispace.test/d/tok123" {
		t.Fatalf("URL must be last line alone, got %q", lines[2])
	}
	// Request inspection: upload then link.
	if f.count() != 2 {
		t.Fatalf("requests %d", f.count())
	}
	up, upBody := f.requests[0], f.bodies[0]
	if up.ContentLength != 3 || string(upBody) != "hi\n" || up.Header.Get("X-File-Name") != "note.txt" || !strings.HasPrefix(up.Header.Get("Content-Type"), "text/plain") {
		t.Fatalf("upload req %v len %d body %q", up.Header, up.ContentLength, upBody)
	}
	link, linkBody := f.requests[1], f.bodies[1]
	if link.URL.Path != "/v1/files/01FILE/links" {
		t.Fatalf("link path %s", link.URL.Path)
	}
	var in map[string]int64
	_ = json.Unmarshal(linkBody, &in)
	if in["expires_in"] != 7200 || in["max_downloads"] != 3 {
		t.Fatalf("link body %s", linkBody)
	}
	// Temp file removed.
	matches, _ := filepath.Glob(filepath.Join(os.TempDir(), "aispace-upload-*"))
	if len(matches) != 0 {
		t.Fatalf("temp files left: %v", matches)
	}
}

func TestUploadStdinDefaultName(t *testing.T) {
	isolate(t)
	f := newFakeServer(t)
	writeConfig(t, config.File{Key: f.key, URL: f.srv.URL})
	r := run("x", "upload", "-")
	if r.code != 0 {
		t.Fatalf("%+v", r)
	}
	req, _ := f.last()
	if req.Header.Get("X-File-Name") != "stdin" {
		t.Fatalf("name %q", req.Header.Get("X-File-Name"))
	}
}

func TestUploadVisibilityOverrides(t *testing.T) {
	isolate(t)
	f := newFakeServer(t)
	writeConfig(t, config.File{Key: f.key, URL: f.srv.URL})

	if r := run("x", "upload", "-"); r.code != 0 {
		t.Fatalf("default upload: %+v", r)
	}
	req, _ := f.last()
	if req.Header.Get("X-File-Visibility") != "" {
		t.Fatalf("default upload should inherit account setting: %v", req.Header)
	}
	if r := run("x", "upload", "-", "--private"); r.code != 0 {
		t.Fatalf("private upload: %+v", r)
	}
	req, _ = f.last()
	if req.Header.Get("X-File-Visibility") != "private" {
		t.Fatalf("private header: %v", req.Header)
	}
	if r := run("x", "upload", "-", "--shared"); r.code != 0 {
		t.Fatalf("shared upload: %+v", r)
	}
	req, _ = f.last()
	if req.Header.Get("X-File-Visibility") != "account" {
		t.Fatalf("shared header: %v", req.Header)
	}
	if r := run("x", "upload", "-", "--private", "--shared"); r.code != ExitUsage {
		t.Fatalf("conflicting visibility flags: %+v", r)
	}
}

func TestDownloadByFileID(t *testing.T) {
	isolate(t)
	f := newFakeServer(t)
	writeConfig(t, config.File{Key: f.key, URL: f.srv.URL})
	output := filepath.Join(t.TempDir(), "shared.txt")
	r := run("", "download", "01SHARED", "--output", output, "--json")
	if r.code != 0 || r.stderr != "" {
		t.Fatalf("%+v", r)
	}
	body, err := os.ReadFile(output)
	if err != nil || string(body) != "shared contents" {
		t.Fatalf("body=%q err=%v", body, err)
	}
	req, _ := f.last()
	if req.URL.Path != "/v1/files/01SHARED/content" {
		t.Fatalf("path %s", req.URL.Path)
	}
	if again := run("", "download", "01SHARED", "--output", output); again.code != ExitUsage {
		t.Fatalf("overwrote output: %+v", again)
	}
}

func TestUploadLinkJSON(t *testing.T) {
	isolate(t)
	f := newFakeServer(t)
	writeConfig(t, config.File{Key: f.key, URL: f.srv.URL})
	r := run("data", "upload", "-", "--name", "d.bin", "--link", "--json")
	if r.code != 0 {
		t.Fatalf("%+v", r)
	}
	var out struct {
		File map[string]any `json:"file"`
		Link map[string]any `json:"link"`
	}
	if err := json.Unmarshal([]byte(r.stdout), &out); err != nil {
		t.Fatal(err)
	}
	if out.File["id"] != "01FILE" || out.Link["url"] != "https://aispace.test/d/tok123" || out.Link["max_downloads"] != nil {
		t.Fatalf("%+v", out)
	}
}

func TestUploadLinkFailureStillReportsFile(t *testing.T) {
	isolate(t)
	f := newFakeServer(t)
	writeConfig(t, config.File{Key: f.key, URL: f.srv.URL})
	f.fail = func(r *http.Request) (int, string) {
		if strings.HasSuffix(r.URL.Path, "/links") {
			return 429, `{"error":{"code":"rate_limited","message":"slow"}}`
		}
		return 0, ""
	}
	r := run("data", "upload", "-", "--link")
	if r.code != ExitRateLimit || !strings.HasPrefix(r.stdout, "uploaded 01FILE") || !strings.Contains(r.stderr, "(rate_limited)") {
		t.Fatalf("%+v", r)
	}
	if f.count() != 2 {
		t.Fatalf("POST link must not be retried: %d requests", f.count())
	}
	r = run("data", "upload", "-", "--link", "--json")
	if r.code != ExitRateLimit || !strings.HasPrefix(r.stdout, `{"file":{`) || strings.Contains(r.stdout, `"link"`) {
		t.Fatalf("%+v", r)
	}
}

func TestUploadQuotaAndSizeErrors(t *testing.T) {
	isolate(t)
	f := newFakeServer(t)
	writeConfig(t, config.File{Key: f.key, URL: f.srv.URL})
	for _, tc := range []struct {
		status int
		body   string
		code   string
	}{
		{402, `{"error":{"code":"quota_exceeded","message":"Key budget exceeded","details":{"remaining":0}}}`, "quota_exceeded"},
		{402, `{"error":{"code":"allowance_exceeded","message":"Account allowance exceeded"}}`, "allowance_exceeded"},
		{400, `{"error":{"code":"file_too_large","message":"Max 25 MB"}}`, "file_too_large"},
		{413, `{"error":{"code":"file_too_large","message":"Max 25 MB"}}`, "file_too_large"},
	} {
		f.fail = func(r *http.Request) (int, string) { return tc.status, tc.body }
		r := run("x", "upload", "-")
		if r.code != ExitQuota || !strings.HasSuffix(strings.TrimSpace(r.stderr), "("+tc.code+")") {
			t.Errorf("%d: %+v", tc.status, r)
		}
		r = run("x", "upload", "-", "--json")
		var e struct {
			Error struct {
				Code    string          `json:"code"`
				Status  int             `json:"status"`
				Details json.RawMessage `json:"details"`
			} `json:"error"`
		}
		if err := json.Unmarshal([]byte(r.stderr), &e); err != nil || e.Error.Code != tc.code || e.Error.Status != tc.status {
			t.Errorf("%d json: %+v %v", tc.status, r, err)
		}
		if tc.code == "quota_exceeded" && string(e.Error.Details) != `{"remaining":0}` {
			t.Errorf("details %s", e.Error.Details)
		}
	}
}

func TestUploadMissingFileAndDirectory(t *testing.T) {
	isolate(t)
	f := newFakeServer(t)
	writeConfig(t, config.File{Key: f.key, URL: f.srv.URL})
	r := run("", "upload", filepath.Join(t.TempDir(), "nope.txt"))
	if r.code != ExitGeneric || !strings.Contains(r.stderr, "(io)") {
		t.Fatalf("%+v", r)
	}
	r = run("", "upload", t.TempDir())
	if r.code != ExitUsage || !strings.Contains(r.stderr, "directory") {
		t.Fatalf("%+v", r)
	}
	if f.count() != 0 {
		t.Fatal("no request should be made")
	}
}

func TestLink(t *testing.T) {
	isolate(t)
	f := newFakeServer(t)
	writeConfig(t, config.File{Key: f.key, URL: f.srv.URL})
	r := run("", "link", "01FILE", "--expires", "30m", "--max-downloads", "1")
	if r.code != 0 {
		t.Fatalf("%+v", r)
	}
	lines := strings.Split(strings.TrimSpace(r.stdout), "\n")
	if len(lines) != 2 || lines[1] != "https://aispace.test/d/tok123" || !strings.HasSuffix(lines[0], "max_downloads 1") {
		t.Fatalf("%q", r.stdout)
	}
	req, body := f.last()
	if req.URL.Path != "/v1/files/01FILE/links" || string(bytes.TrimSpace(body)) != `{"expires_in":1800,"max_downloads":1}` {
		t.Fatalf("%s %s", req.URL.Path, body)
	}
	r = run("", "link", "01FILE", "--json")
	if r.code != 0 || !strings.HasPrefix(r.stdout, `{"created_at":`) {
		t.Fatalf("%+v", r)
	}
	_, body = f.last()
	if string(bytes.TrimSpace(body)) != `{}` {
		t.Fatalf("body %s", body)
	}
	// Not found -> exit 1.
	f.fail = func(r *http.Request) (int, string) {
		return 404, `{"error":{"code":"not_found","message":"no such file"}}`
	}
	r = run("", "link", "01NOPE")
	if r.code != ExitGeneric || strings.TrimSpace(r.stderr) != "error: no such file (not_found)" {
		t.Fatalf("%+v", r)
	}
}

func TestLsPaginatesAndRetries429(t *testing.T) {
	isolate(t)
	f := newFakeServer(t)
	writeConfig(t, config.File{Key: f.key, URL: f.srv.URL})
	f.status429Once["GET /v1/files"] = true

	r := run("", "ls")
	if r.code != 0 {
		t.Fatalf("%+v", r)
	}
	want := "01A 5 B 2025-09-11T15:33:20Z a.txt\n01B 2.0 KB 2025-09-11T15:33:20Z b.pdf\n"
	if r.stdout != want {
		t.Fatalf("stdout %q", r.stdout)
	}
	// 429 + retry + page 2 = 3 requests.
	if f.count() != 3 {
		t.Fatalf("requests %d", f.count())
	}

	r = run("", "ls", "--json")
	if r.code != 0 {
		t.Fatalf("%+v", r)
	}
	var out struct {
		Files      []map[string]any `json:"files"`
		NextCursor *string          `json:"next_cursor"`
	}
	if err := json.Unmarshal([]byte(r.stdout), &out); err != nil || len(out.Files) != 2 || out.NextCursor != nil || out.Files[1]["id"] != "01B" {
		t.Fatalf("%q %v", r.stdout, err)
	}
}

func TestLsEmpty(t *testing.T) {
	isolate(t)
	f := newFakeServer(t)
	writeConfig(t, config.File{Key: f.key, URL: f.srv.URL})
	f.fail = func(r *http.Request) (int, string) { return 200, `{"files":[],"next_cursor":null}` }
	r := run("", "ls")
	if r.code != 0 || r.stdout != "" || !strings.Contains(r.stderr, "no files") {
		t.Fatalf("%+v", r)
	}
	r = run("", "ls", "--json")
	if r.code != 0 || strings.TrimSpace(r.stdout) != `{"files":[],"next_cursor":null}` {
		t.Fatalf("%+v", r)
	}
}

func TestLsRateLimitedTwiceExits5(t *testing.T) {
	isolate(t)
	f := newFakeServer(t)
	writeConfig(t, config.File{Key: f.key, URL: f.srv.URL})
	f.fail = func(r *http.Request) (int, string) {
		return 429, `{"error":{"code":"rate_limited","message":"slow down"}}`
	}
	r := run("", "ls")
	if r.code != ExitRateLimit || strings.TrimSpace(r.stderr) != "error: slow down (rate_limited)" {
		t.Fatalf("%+v", r)
	}
	if f.count() != 2 {
		t.Fatalf("expected exactly one retry, got %d requests", f.count())
	}
}

func TestRmAndRevoke(t *testing.T) {
	isolate(t)
	f := newFakeServer(t)
	writeConfig(t, config.File{Key: f.key, URL: f.srv.URL})
	r := run("", "rm", "01A")
	if r.code != 0 || r.stdout != "deleted 01A\n" {
		t.Fatalf("%+v", r)
	}
	req, _ := f.last()
	if req.Method != "DELETE" || req.URL.Path != "/v1/files/01A" {
		t.Fatalf("%s %s", req.Method, req.URL.Path)
	}
	r = run("", "revoke", "01L", "--json")
	if r.code != 0 || strings.TrimSpace(r.stdout) != `{"revoked":"01L"}` {
		t.Fatalf("%+v", r)
	}
	req, _ = f.last()
	if req.Method != "DELETE" || req.URL.Path != "/v1/links/01L" {
		t.Fatalf("%s %s", req.Method, req.URL.Path)
	}
	// Revoked key on a DELETE: 401 -> exit 3, no retry.
	f.fail = func(r *http.Request) (int, string) {
		return 401, `{"error":{"code":"key_revoked","message":"Key revoked"}}`
	}
	before := f.count()
	r = run("", "rm", "01A")
	if r.code != ExitAuth || !strings.Contains(r.stderr, "(key_revoked)") || f.count() != before+1 {
		t.Fatalf("%+v", r)
	}
}

func TestQuota(t *testing.T) {
	isolate(t)
	f := newFakeServer(t)
	writeConfig(t, config.File{Key: f.key, URL: f.srv.URL})
	r := run("", "quota")
	if r.code != 0 {
		t.Fatalf("%+v", r)
	}
	for _, want := range []string{
		"key: used 1.0 MB of 50.0 MB, 49.0 MB remaining",
		"account: used 3.0 MB of 100.0 MB, 97.0 MB remaining, plan free (extra blocks 0)",
		"month: uploads 12/100, downloads 340/1000, resets 2025-10-01T00:00:00Z",
		"limits: max file 25.0 MB, max file ttl 30d, max link ttl 7d, uploads 60/h 500/d, requests 300/min",
		"rate: uploads remaining 58 this hour, 490 today; requests remaining 299 this minute",
	} {
		if !strings.Contains(r.stdout, want) {
			t.Errorf("missing %q in %q", want, r.stdout)
		}
	}
	r = run("", "quota", "--json")
	if r.code != 0 || !strings.HasPrefix(r.stdout, `{"key":{"budget_bytes":52428800`) {
		t.Fatalf("%+v", r)
	}
}

func TestWhoamiJSONAndAuthFailure(t *testing.T) {
	isolate(t)
	f := newFakeServer(t)
	writeConfig(t, config.File{Key: f.key, URL: f.srv.URL})
	r := run("", "whoami", "--json")
	if r.code != 0 || !strings.Contains(r.stdout, `"email":"luigi@example.com"`) {
		t.Fatalf("%+v", r)
	}
	r = run("", "whoami", "--key", "ask_bad")
	if r.code != ExitAuth {
		t.Fatalf("%+v", r)
	}
}

func TestServerErrorExit1(t *testing.T) {
	isolate(t)
	f := newFakeServer(t)
	writeConfig(t, config.File{Key: f.key, URL: f.srv.URL})
	f.fail = func(r *http.Request) (int, string) { return 500, `{"error":{"code":"internal","message":"boom"}}` }
	r := run("", "quota")
	if r.code != ExitGeneric || strings.TrimSpace(r.stderr) != "error: boom (internal)" {
		t.Fatalf("%+v", r)
	}
}

func TestInfo(t *testing.T) {
	isolate(t)
	f := newFakeServer(t)
	writeConfig(t, config.File{Key: f.key, URL: f.srv.URL})
	r := run("", "info", "01A")
	if r.code != 0 || r.stdout != "01A a.txt 5 B text/plain created 2025-09-04T15:33:20Z expires 2025-09-11T15:33:20Z sha256 deadbeef encryption none visibility account\n" {
		t.Fatalf("%+v", r)
	}
	req, _ := f.last()
	if req.Method != "GET" || req.URL.Path != "/v1/files/01A" {
		t.Fatalf("%s %s", req.Method, req.URL.Path)
	}
	r = run("", "info", "01A", "--json")
	if r.code != 0 || !strings.HasPrefix(r.stdout, `{"id":"01A"`) {
		t.Fatalf("%+v", r)
	}
	// GET is retried once on 429.
	f.status429Once["GET /v1/files/01A"] = true
	before := f.count()
	if r = run("", "info", "01A"); r.code != 0 || f.count() != before+2 {
		t.Fatalf("%+v count %d", r, f.count()-before)
	}
}

func TestLinks(t *testing.T) {
	isolate(t)
	f := newFakeServer(t)
	writeConfig(t, config.File{Key: f.key, URL: f.srv.URL})
	r := run("", "links", "01A")
	if r.code != 0 {
		t.Fatalf("%+v", r)
	}
	want := "01L1 expires 2025-09-04T16:33:20Z downloads 1/3\n01L2 expires 2025-09-04T16:33:20Z downloads 0/unlimited revoked\n"
	if r.stdout != want {
		t.Fatalf("stdout %q", r.stdout)
	}
	req, _ := f.last()
	if req.Method != "GET" || req.URL.Path != "/v1/files/01A/links" {
		t.Fatalf("%s %s", req.Method, req.URL.Path)
	}
	r = run("", "links", "01A", "--json")
	if r.code != 0 || !strings.HasPrefix(r.stdout, `{"links":[`) {
		t.Fatalf("%+v", r)
	}
	f.fail = func(r *http.Request) (int, string) { return 200, `{"links":[]}` }
	r = run("", "links", "01A")
	if r.code != 0 || r.stdout != "" || !strings.Contains(r.stderr, "no links") {
		t.Fatalf("%+v", r)
	}
}

func TestLsAllFlagNoop(t *testing.T) {
	isolate(t)
	f := newFakeServer(t)
	writeConfig(t, config.File{Key: f.key, URL: f.srv.URL})
	r := run("", "ls", "--all")
	if r.code != 0 || !strings.Contains(r.stdout, "01B") {
		t.Fatalf("%+v", r)
	}
}

func TestUploadContentTypeFlag(t *testing.T) {
	isolate(t)
	f := newFakeServer(t)
	writeConfig(t, config.File{Key: f.key, URL: f.srv.URL})
	r := run("{}", "upload", "-", "--name", "x.bin", "--content-type", "application/json")
	if r.code != 0 {
		t.Fatalf("%+v", r)
	}
	req, _ := f.last()
	if req.Header.Get("Content-Type") != "application/json" {
		t.Fatalf("ct %q", req.Header.Get("Content-Type"))
	}
}

func TestRmRevokeMultiple(t *testing.T) {
	isolate(t)
	f := newFakeServer(t)
	writeConfig(t, config.File{Key: f.key, URL: f.srv.URL})
	r := run("", "rm", "01A", "01B", "--json")
	if r.code != 0 || r.stdout != "{\"deleted\":\"01A\"}\n{\"deleted\":\"01B\"}\n" {
		t.Fatalf("%+v", r)
	}
	r = run("", "revoke", "L1", "L2", "L3")
	if r.code != 0 || r.stdout != "revoked L1\nrevoked L2\nrevoked L3\n" {
		t.Fatalf("%+v", r)
	}
	// Failure midway stops and names the ID, keeping the API exit code.
	f.fail = func(r *http.Request) (int, string) {
		if r.URL.Path == "/v1/files/BAD" {
			return 404, `{"error":{"code":"not_found","message":"no such file"}}`
		}
		return 0, ""
	}
	r = run("", "rm", "01A", "BAD", "01B")
	if r.code != ExitGeneric || r.stdout != "deleted 01A\n" || strings.TrimSpace(r.stderr) != "error: BAD: no such file (not_found)" {
		t.Fatalf("%+v", r)
	}
}

func TestCompletionCommandExists(t *testing.T) {
	isolate(t)
	r := run("", "completion", "bash")
	if r.code != 0 || !strings.Contains(r.stdout, "aispace") {
		t.Fatalf("%+v", r)
	}
}

func TestFmtBytes(t *testing.T) {
	for in, want := range map[int64]string{0: "0 B", 1023: "1023 B", 1024: "1.0 KB", 1536: "1.5 KB", 1048576: "1.0 MB", 1073741824: "1.0 GB"} {
		if got := fmtBytes(in); got != want {
			t.Errorf("fmtBytes(%d) = %q, want %q", in, got, want)
		}
	}
}

// A control character in any value that becomes an HTTP header used to reach
// the transport, which failed with "invalid header field value". That surfaced
// as exit 1 with code "network", telling an agent to retry a request that can
// never succeed. All of these are bad input and must exit 2.
func TestControlCharactersInHeaderValuesAreUsageErrors(t *testing.T) {
	isolate(t)
	f := newFakeServer(t)
	writeConfig(t, config.File{Key: f.key, URL: f.srv.URL})
	path := filepath.Join(t.TempDir(), "ok.txt")
	if err := os.WriteFile(path, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name string
		args []string
		want string
	}{
		{"name with newline", []string{"upload", path, "--name", "a\nb.txt"}, "file name must not contain control characters"},
		{"name with CR", []string{"upload", path, "--name", "a\rb.txt"}, "file name must not contain control characters"},
		{"content type with newline", []string{"upload", path, "--content-type", "text/x\ny"}, "--content-type must not contain control characters"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := run("", c.args...)
			if r.code != ExitUsage {
				t.Fatalf("exit = %d, want %d: %+v", r.code, ExitUsage, r)
			}
			if !strings.Contains(r.stderr, c.want) {
				t.Fatalf("stderr = %q, want it to contain %q", r.stderr, c.want)
			}
			if strings.Contains(r.stderr, "network") {
				t.Fatalf("bad input reported as a network fault: %q", r.stderr)
			}
		})
	}
	if f.count() != 0 {
		t.Fatalf("no request should have been sent, got %d", f.count())
	}
}

func TestHeaderValidationPrecedesReadingStdin(t *testing.T) {
	isolate(t)
	f := newFakeServer(t)
	writeConfig(t, config.File{Key: f.key, URL: f.srv.URL})

	cases := [][]string{
		{"upload", "-", "--name", "a\nb.txt"},
		{"upload", "-", "--content-type", "text/x\ny"},
	}
	for _, args := range cases {
		var stdout, stderr bytes.Buffer
		code := Run(args, iotest.ErrReader(errors.New("stdin must not be read")), &stdout, &stderr, "1.2.3")
		if code != ExitUsage {
			t.Errorf("%q: exit = %d, want %d; stderr = %q", args, code, ExitUsage, stderr.String())
		}
	}
	if f.count() != 0 {
		t.Fatalf("no request should have been sent, got %d", f.count())
	}
}

// A key read from a file often keeps a trailing newline, and on Windows a CRLF
// file leaves a \r. That cannot go into an Authorization header.
func TestControlCharacterInKeyIsUsageError(t *testing.T) {
	isolate(t)
	f := newFakeServer(t)
	t.Setenv(config.EnvURL, f.srv.URL)

	for _, suffix := range []string{"\n", "\r", "\r\n"} {
		key := f.key + suffix
		t.Setenv(config.EnvKey, key)
		for _, cmd := range []string{"quota", "ls", "whoami", "login"} {
			r := run("", cmd)
			if r.code != ExitUsage {
				t.Errorf("%s with key%q: exit = %d, want %d (%+v)", cmd, suffix, r.code, ExitUsage, r)
			}
			if !strings.Contains(r.stderr, "control character") {
				t.Errorf("%s with key%q: stderr = %q", cmd, suffix, r.stderr)
			}
			// The key is a credential and must not be echoed back.
			if strings.Contains(r.stdout+r.stderr, f.key) {
				t.Errorf("%s with key%q leaked the key: %q", cmd, suffix, r.stdout+r.stderr)
			}
		}
	}
	if f.count() != 0 {
		t.Fatalf("no request should have been sent, got %d", f.count())
	}
}

// Values that are unusual but legal in a header must keep working.
func TestHeaderSafeValuesStillAccepted(t *testing.T) {
	isolate(t)
	f := newFakeServer(t)
	writeConfig(t, config.File{Key: f.key, URL: f.srv.URL})
	path := filepath.Join(t.TempDir(), "ok.txt")
	if err := os.WriteFile(path, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"café.txt", "a b.txt", "a\tb.txt", "sürprise-résumé.pdf"} {
		r := run("", "upload", path, "--name", name)
		if r.code != ExitOK {
			t.Errorf("upload --name %q = %+v, want success", name, r)
		}
	}
}
