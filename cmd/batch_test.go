package cmd

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/aispace-sh/aispace-client/internal/config"
)

// batchServer deletes anything except IDs containing "missing", which 404, and
// fails everything with 401 once unauthorized is set.
type batchServer struct {
	mu                sync.Mutex
	srv               *httptest.Server
	key               string
	unauthorized      bool
	unauthorizedAfter int
	seen              []string
}

func newBatchServer(t *testing.T) *batchServer {
	t.Helper()
	b := &batchServer{key: "ask_validkey0000000000000000000000"}
	b.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.URL.Path[strings.LastIndexByte(r.URL.Path, '/')+1:]
		b.mu.Lock()
		b.seen = append(b.seen, id)
		unauth := b.unauthorized || b.unauthorizedAfter > 0 && len(b.seen) > b.unauthorizedAfter
		b.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		if unauth {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":{"code":"invalid_key","message":"Invalid key"}}`))
			return
		}
		if strings.Contains(id, "missing") {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":{"code":"not_found","message":"No such id"}}`))
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(b.srv.Close)
	return b
}

func (b *batchServer) attempted() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string(nil), b.seen...)
}

// Without --continue the first failure must still stop the run, so existing
// callers keep the behaviour they were written against.
func TestRmStopsAtFirstFailureByDefault(t *testing.T) {
	isolate(t)
	b := newBatchServer(t)
	writeConfig(t, config.File{Key: b.key, URL: b.srv.URL})

	r := run("", "rm", "keep1", "missing2", "keep3")
	if r.code != ExitGeneric {
		t.Fatalf("exit = %d, want %d: %+v", r.code, ExitGeneric, r)
	}
	if got := b.attempted(); len(got) != 2 || got[1] != "missing2" {
		t.Fatalf("attempted %v, want to stop after missing2", got)
	}
	if !strings.Contains(r.stdout, "deleted keep1") || strings.Contains(r.stdout, "keep3") {
		t.Fatalf("stdout = %q", r.stdout)
	}
}

// --continue attempts every ID and still fails overall.
func TestRmContinuePastMissingIDs(t *testing.T) {
	isolate(t)
	b := newBatchServer(t)
	writeConfig(t, config.File{Key: b.key, URL: b.srv.URL})

	r := run("", "rm", "--continue", "keep1", "missing2", "keep3")
	if r.code != ExitGeneric {
		t.Fatalf("exit = %d, want %d: %+v", r.code, ExitGeneric, r)
	}
	if got := b.attempted(); len(got) != 3 {
		t.Fatalf("attempted %v, want all three", got)
	}
	for _, want := range []string{"deleted keep1", "deleted keep3"} {
		if !strings.Contains(r.stdout, want) {
			t.Errorf("stdout missing %q: %q", want, r.stdout)
		}
	}
	if !strings.Contains(r.stderr, "missing2") || !strings.Contains(r.stderr, "not_found") {
		t.Errorf("stderr should name the failed ID: %q", r.stderr)
	}
	// The failure is reported once, not again by the top-level handler.
	if n := strings.Count(r.stderr, "not_found"); n != 1 {
		t.Errorf("failure reported %d times, want 1: %q", n, r.stderr)
	}
}

// A rejected key applies to every remaining ID, so --continue must not keep
// hammering the API with requests that cannot succeed.
func TestRmContinueStopsOnUnauthorized(t *testing.T) {
	isolate(t)
	b := newBatchServer(t)
	b.unauthorized = true
	writeConfig(t, config.File{Key: b.key, URL: b.srv.URL})

	r := run("", "rm", "--continue", "a", "b", "c", "d")
	if r.code != ExitAuth {
		t.Fatalf("exit = %d, want %d: %+v", r.code, ExitAuth, r)
	}
	if got := b.attempted(); len(got) != 1 {
		t.Fatalf("attempted %v, want to give up after the first 401", got)
	}
}

func TestRmContinueReportsLaterFatalError(t *testing.T) {
	isolate(t)
	b := newBatchServer(t)
	b.unauthorizedAfter = 1
	writeConfig(t, config.File{Key: b.key, URL: b.srv.URL})

	r := run("", "rm", "--continue", "missing1", "second", "third")
	if r.code != ExitAuth {
		t.Fatalf("exit = %d, want %d: %+v", r.code, ExitAuth, r)
	}
	if got := b.attempted(); len(got) != 2 {
		t.Fatalf("attempted %v, want to stop after the later 401", got)
	}
	if !strings.Contains(r.stderr, "missing1: No such id") || !strings.Contains(r.stderr, "second: Invalid key") {
		t.Fatalf("stderr must report both attempted failures: %q", r.stderr)
	}
	if strings.Contains(r.stderr, "third") {
		t.Fatalf("unattempted ID was reported: %q", r.stderr)
	}
}

func TestRevokeContinuePastMissingIDs(t *testing.T) {
	isolate(t)
	b := newBatchServer(t)
	writeConfig(t, config.File{Key: b.key, URL: b.srv.URL})

	r := run("", "revoke", "--continue", "link1", "missinglink", "link3")
	if r.code != ExitGeneric {
		t.Fatalf("exit = %d, want %d: %+v", r.code, ExitGeneric, r)
	}
	if got := b.attempted(); len(got) != 3 {
		t.Fatalf("attempted %v, want all three", got)
	}
	if !strings.Contains(r.stdout, "revoked link1") || !strings.Contains(r.stdout, "revoked link3") {
		t.Errorf("stdout = %q", r.stdout)
	}
}

// Successes stay machine-readable on stdout while the failure goes to stderr as
// a JSON error object, so a caller parsing stdout is not handed a mixed stream.
func TestRmContinueJSONKeepsStreamsSeparate(t *testing.T) {
	isolate(t)
	b := newBatchServer(t)
	writeConfig(t, config.File{Key: b.key, URL: b.srv.URL})

	r := run("", "rm", "--continue", "--json", "keep1", "missing2", "keep3")
	if r.code != ExitGeneric {
		t.Fatalf("exit = %d: %+v", r.code, r)
	}
	wantOut := `{"deleted":"keep1"}` + "\n" + `{"deleted":"keep3"}` + "\n"
	if r.stdout != wantOut {
		t.Fatalf("stdout = %q, want %q", r.stdout, wantOut)
	}
	if !strings.Contains(r.stderr, `"code":"not_found"`) || !strings.Contains(r.stderr, `"exit_code":1`) {
		t.Fatalf("stderr = %q", r.stderr)
	}
	if n := strings.Count(r.stderr, `"error"`); n != 1 {
		t.Fatalf("error reported %d times, want 1: %q", n, r.stderr)
	}
}

// Every ID succeeding must still exit 0.
func TestRmContinueAllSucceed(t *testing.T) {
	isolate(t)
	b := newBatchServer(t)
	writeConfig(t, config.File{Key: b.key, URL: b.srv.URL})

	r := run("", "rm", "--continue", "a", "b")
	if r.code != ExitOK || r.stderr != "" {
		t.Fatalf("%+v", r)
	}
}
