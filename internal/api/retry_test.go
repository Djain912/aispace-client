package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// flakyServer fails the first n requests with the given status, then succeeds.
type flakyServer struct {
	mu       sync.Mutex
	srv      *httptest.Server
	status   int
	failures int
	attempts int
	method   string
}

func newFlakyServer(t *testing.T, status, failures int) *flakyServer {
	t.Helper()
	f := &flakyServer{status: status, failures: failures}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.attempts++
		n := f.attempts
		f.method = r.Method
		f.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		if n <= f.failures {
			w.WriteHeader(f.status)
			_, _ = w.Write([]byte(`{"error":{"code":"upstream","message":"backend unavailable"}}`))
			return
		}
		if strings.HasSuffix(r.URL.Path, "/content") {
			_, _ = w.Write([]byte("payload"))
			return
		}
		_, _ = w.Write([]byte(`{"key":{"budget_bytes":1,"used_bytes":0,"remaining_bytes":1}}`))
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *flakyServer) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.attempts
}

func flakyClient(f *flakyServer) *Client {
	c := New(f.srv.URL, "ask_test", "test-agent")
	c.Sleep = func(time.Duration) {}
	return c
}

// A gateway that is briefly between healthy backends should not end an agent's
// workflow: the read did not change anything, so one more attempt is safe.
func TestTransientServerErrorIsRetriedOnGET(t *testing.T) {
	for _, status := range []int{
		http.StatusBadGateway,
		http.StatusServiceUnavailable,
		http.StatusGatewayTimeout,
	} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			f := newFlakyServer(t, status, 1)
			if _, err := flakyClient(f).Quota(context.Background()); err != nil {
				t.Fatalf("expected the retry to succeed: %v", err)
			}
			if n := f.count(); n != 2 {
				t.Fatalf("attempts = %d, want 2 (one failure then one retry)", n)
			}
		})
	}
}

// The retry budget is one. A backend that is properly down must surface as an
// error rather than being hammered.
func TestTransientServerErrorIsRetriedOnlyOnce(t *testing.T) {
	f := newFlakyServer(t, http.StatusServiceUnavailable, 5)
	_, err := flakyClient(f).Quota(context.Background())
	if err == nil {
		t.Fatal("expected an error once the retry was used up")
	}
	var apiErr *Error
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusServiceUnavailable {
		t.Fatalf("error = %v, want a 503", err)
	}
	if n := f.count(); n != 2 {
		t.Fatalf("attempts = %d, want 2", n)
	}
}

// 500 usually means the request itself is the problem, so repeating it only
// doubles the load and returns the same answer.
func TestInternalServerErrorIsNotRetried(t *testing.T) {
	f := newFlakyServer(t, http.StatusInternalServerError, 1)
	if _, err := flakyClient(f).Quota(context.Background()); err == nil {
		t.Fatal("expected the 500 to surface")
	}
	if n := f.count(); n != 1 {
		t.Fatalf("attempts = %d, want 1", n)
	}
}

// Authenticated downloads consume monthly allowance. A gateway error may
// arrive after the server counted the request, so replaying it could charge the
// account twice.
func TestTransientServerErrorIsNotRetriedOnDownload(t *testing.T) {
	f := newFlakyServer(t, http.StatusBadGateway, 1)
	_, err := flakyClient(f).Download(context.Background(), "01A")
	if err == nil {
		t.Fatal("expected the gateway error to surface")
	}
	if n := f.count(); n != 1 {
		t.Fatalf("attempts = %d, want 1; a metered download must not be replayed", n)
	}
}

// Writes are not idempotent: a 503 may still have been applied, so repeating an
// upload could store the file twice.
func TestTransientServerErrorIsNotRetriedOnPOST(t *testing.T) {
	f := newFlakyServer(t, http.StatusServiceUnavailable, 1)
	_, err := flakyClient(f).CreateLink(context.Background(), "01A", LinkOptions{})
	if err == nil {
		t.Fatal("expected the 503 to surface")
	}
	if n := f.count(); n != 1 {
		t.Fatalf("attempts = %d, want 1; a write must not be repeated", n)
	}
	if f.method != http.MethodPost {
		t.Fatalf("method = %s", f.method)
	}
}
