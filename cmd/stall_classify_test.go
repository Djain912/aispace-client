package cmd

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aispace-sh/aispace-client/internal/api"
	"github.com/aispace-sh/aispace-client/internal/config"
)

// A stall reaches the command wrapped as an I/O failure, because that is what
// io.Copy reports while writing the output. Coding it "io" hides the reason the
// transfer stopped, and disagrees with the "timeout" the client reports when a
// transfer stalls before the body starts.
func TestClassifyReportsStalledTransferAsTimeout(t *testing.T) {
	a := &app{}
	wrapped := &codedError{
		code: "io",
		err:  fmt.Errorf("writing output: %w", api.ErrTransferStalled),
		exit: ExitGeneric,
	}
	exit, code := a.classify(wrapped)
	if code != "timeout" {
		t.Fatalf("code = %q, want %q", code, "timeout")
	}
	if exit != ExitGeneric {
		t.Fatalf("exit = %d, want %d", exit, ExitGeneric)
	}
}

// An ordinary I/O failure must keep its own code.
func TestClassifyKeepsPlainIOCode(t *testing.T) {
	a := &app{}
	exit, code := a.classify(&codedError{code: "io", err: os.ErrPermission, exit: ExitGeneric})
	if code != "io" || exit != ExitGeneric {
		t.Fatalf("classify = %d/%q, want %d/io", exit, code, ExitGeneric)
	}
}

// End to end: a server that sends headers and a few bytes and then goes quiet
// must end the download as a timeout, not as a generic write failure.
func TestDownloadStallIsReportedAsTimeout(t *testing.T) {
	isolate(t)
	old := inactivityTimeout
	inactivityTimeout = 150 * time.Millisecond
	t.Cleanup(func() { inactivityTimeout = old })

	release := make(chan struct{})
	key := "ask_validkey0000000000000000000000"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+key {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Length", "1000")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("start"))
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		// Go quiet: never send the rest of the promised body.
		select {
		case <-release:
		case <-time.After(10 * time.Second):
		}
	}))
	// Cleanups run last-registered-first, and srv.Close waits for in-flight
	// handlers, so the handler has to be released before it runs.
	t.Cleanup(srv.Close)
	t.Cleanup(func() { close(release) })
	writeConfig(t, config.File{Key: key, URL: srv.URL})

	out := filepath.Join(t.TempDir(), "stalled.bin")
	r := run("", "download", "01A", "--output", out)
	if r.code != ExitGeneric {
		t.Fatalf("exit = %d, want %d: %+v", r.code, ExitGeneric, r)
	}
	if !strings.Contains(r.stderr, "(timeout)") {
		t.Fatalf("stderr = %q, want it coded as a timeout", r.stderr)
	}
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Fatalf("partial download was left on disk: err=%v", err)
	}
}
