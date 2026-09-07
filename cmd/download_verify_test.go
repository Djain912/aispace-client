package cmd

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/aispace-sh/aispace-client/internal/config"
)

// verifyServer serves one file plus its metadata, so a test can decide whether
// the recorded digest matches the bytes actually served.
type verifyServer struct {
	mu          sync.Mutex
	srv         *httptest.Server
	key         string
	content     []byte
	recorded    string // the sha256 the metadata reports
	metaHits    int
	contentHits int
}

func newVerifyServer(t *testing.T, content string, recorded string) *verifyServer {
	t.Helper()
	v := &verifyServer{key: "ask_validkey0000000000000000000000", content: []byte(content), recorded: recorded}
	v.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+v.key {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":{"code":"invalid_key","message":"Invalid key"}}`))
			return
		}
		switch {
		case strings.HasSuffix(r.URL.Path, "/content"):
			v.mu.Lock()
			v.contentHits++
			v.mu.Unlock()
			w.Header().Set("Content-Type", "application/octet-stream")
			_, _ = w.Write(v.content)
		case strings.HasPrefix(r.URL.Path, "/v1/files/"):
			v.mu.Lock()
			v.metaHits++
			v.mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			meta := map[string]any{
				"id": "01A", "name": "a.bin", "content_type": "application/octet-stream",
				"size_bytes": len(v.content), "visibility": "account",
				"created_at": 1757000000, "expires_at": 1757604800,
			}
			if v.recorded != "" {
				meta["sha256"] = v.recorded
			}
			_ = json.NewEncoder(w).Encode(meta)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(v.srv.Close)
	return v
}

func (v *verifyServer) counts() (int, int) {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.metaHits, v.contentHits
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func TestDownloadVerifyAcceptsMatchingDigest(t *testing.T) {
	isolate(t)
	const body = "the real contents"
	v := newVerifyServer(t, body, sha256Hex(body))
	writeConfig(t, config.File{Key: v.key, URL: v.srv.URL})
	out := filepath.Join(t.TempDir(), "a.bin")

	r := run("", "download", "01A", "--output", out, "--verify")
	if r.code != ExitOK {
		t.Fatalf("%+v", r)
	}
	if !strings.Contains(r.stdout, "sha256 verified") {
		t.Errorf("stdout should report the check: %q", r.stdout)
	}
	got, err := os.ReadFile(out)
	if err != nil || string(got) != body {
		t.Fatalf("content = %q err=%v", got, err)
	}
	if meta, content := v.counts(); meta != 1 || content != 1 {
		t.Errorf("requests: metadata=%d content=%d, want 1 and 1", meta, content)
	}
}

// A digest that does not match must leave nothing behind: a caller that ignores
// the exit code would otherwise read a corrupt file as if it were good.
func TestDownloadVerifyRemovesCorruptOutput(t *testing.T) {
	isolate(t)
	v := newVerifyServer(t, "tampered contents", sha256Hex("the real contents"))
	writeConfig(t, config.File{Key: v.key, URL: v.srv.URL})
	out := filepath.Join(t.TempDir(), "a.bin")

	r := run("", "download", "01A", "--output", out, "--verify")
	if r.code != ExitGeneric {
		t.Fatalf("exit = %d, want %d: %+v", r.code, ExitGeneric, r)
	}
	if !strings.Contains(r.stderr, "checksum mismatch") {
		t.Errorf("stderr = %q", r.stderr)
	}
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Fatalf("corrupt output was left on disk: err=%v", err)
	}
}

// Without a recorded digest there is nothing to compare against; say so instead
// of reporting success for a check that never happened.
func TestDownloadVerifyRefusesWhenNoDigestRecorded(t *testing.T) {
	isolate(t)
	v := newVerifyServer(t, "contents", "")
	writeConfig(t, config.File{Key: v.key, URL: v.srv.URL})
	out := filepath.Join(t.TempDir(), "a.bin")

	r := run("", "download", "01A", "--output", out, "--verify")
	if r.code != ExitGeneric {
		t.Fatalf("exit = %d, want %d: %+v", r.code, ExitGeneric, r)
	}
	if !strings.Contains(r.stderr, "no recorded SHA-256") {
		t.Errorf("stderr = %q", r.stderr)
	}
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Errorf("nothing should have been written")
	}
	// The body is not worth transferring when it can never be checked.
	if _, content := v.counts(); content != 0 {
		t.Errorf("content was downloaded anyway (%d requests)", content)
	}
}

// The default path must be untouched: same bytes, and no extra metadata call.
func TestDownloadWithoutVerifyMakesNoExtraRequest(t *testing.T) {
	isolate(t)
	const body = "the real contents"
	v := newVerifyServer(t, body, sha256Hex(body))
	writeConfig(t, config.File{Key: v.key, URL: v.srv.URL})
	out := filepath.Join(t.TempDir(), "a.bin")

	r := run("", "download", "01A", "--output", out)
	if r.code != ExitOK {
		t.Fatalf("%+v", r)
	}
	got, _ := os.ReadFile(out)
	if string(got) != body {
		t.Fatalf("content = %q", got)
	}
	if meta, content := v.counts(); meta != 0 || content != 1 {
		t.Errorf("requests: metadata=%d content=%d, want 0 and 1", meta, content)
	}
}

func TestDownloadVerifyJSONReportsDigest(t *testing.T) {
	isolate(t)
	const body = "the real contents"
	v := newVerifyServer(t, body, sha256Hex(body))
	writeConfig(t, config.File{Key: v.key, URL: v.srv.URL})
	out := filepath.Join(t.TempDir(), "a.bin")

	r := run("", "download", "01A", "--output", out, "--verify", "--json")
	if r.code != ExitOK {
		t.Fatalf("%+v", r)
	}
	var got map[string]string
	if err := json.Unmarshal([]byte(r.stdout), &got); err != nil {
		t.Fatalf("stdout %q: %v", r.stdout, err)
	}
	if got["sha256"] != sha256Hex(body) || got["output"] != out || got["file_id"] != "01A" {
		t.Fatalf("json = %v", got)
	}
}

// stdout cannot be recalled, so the exit code carries the result and the
// message must not pretend the bytes were withheld.
func TestDownloadVerifyToStdoutStillReportsMismatch(t *testing.T) {
	isolate(t)
	v := newVerifyServer(t, "tampered", sha256Hex("real"))
	writeConfig(t, config.File{Key: v.key, URL: v.srv.URL})

	r := run("", "download", "01A", "--output", "-", "--verify")
	if r.code != ExitGeneric {
		t.Fatalf("exit = %d, want %d: %+v", r.code, ExitGeneric, r)
	}
	if r.stdout != "tampered" {
		t.Errorf("stdout = %q, want the streamed bytes", r.stdout)
	}
	if !strings.Contains(r.stderr, "already written to stdout") {
		t.Errorf("stderr should say the bytes escaped: %q", r.stderr)
	}
}

// Digests are hex; comparison must not depend on the server's casing.
func TestDownloadVerifyIsCaseInsensitive(t *testing.T) {
	isolate(t)
	const body = "the real contents"
	v := newVerifyServer(t, body, strings.ToUpper(sha256Hex(body)))
	writeConfig(t, config.File{Key: v.key, URL: v.srv.URL})
	out := filepath.Join(t.TempDir(), "a.bin")

	if r := run("", "download", "01A", "--output", out, "--verify"); r.code != ExitOK {
		t.Fatalf("uppercase digest rejected: %+v", r)
	}
}
