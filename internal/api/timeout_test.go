package api

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// trickle sends response headers at once, then writes the body in small pieces
// over the given span. That is what a large file over a slow link looks like:
// progress is steady, it just takes a while.
func trickle(t *testing.T, pieces int, gap time.Duration) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.WriteHeader(http.StatusOK)
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Error("test server cannot flush")
			return
		}
		for i := 0; i < pieces; i++ {
			if _, err := w.Write([]byte("chunk")); err != nil {
				return
			}
			flusher.Flush()
			time.Sleep(gap)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// A transfer that keeps making progress must not be cut off, however long it
// runs. Under a single deadline covering the body this failed part-way with
// "Client.Timeout ... while reading body", which made the cap a bandwidth floor
// rather than a liveness check.
func TestSlowButProgressingTransferIsNotCutOff(t *testing.T) {
	srv := trickle(t, 6, 40*time.Millisecond)
	c := newHTTPClient(time.Second, time.Second, 50*time.Millisecond)

	resp, err := c.Get(srv.URL)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading body: %v", err)
	}
	if len(body) != 30 {
		t.Fatalf("read %d bytes (%q), want the whole body", len(body), body)
	}
}

// The body is patient, but a server that accepts the connection and then says
// nothing must still be given up on.
func TestUnresponsiveServerFailsFast(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			// Hold the connection open and never write a response.
			t.Cleanup(func() { _ = conn.Close() })
		}
	}()

	c := newHTTPClient(time.Second, time.Second, 100*time.Millisecond)
	start := time.Now()
	_, err = c.Get("http://" + ln.Addr().String())
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("expected the request to fail")
	}
	if !strings.Contains(err.Error(), "timeout awaiting response headers") {
		t.Fatalf("error = %v, want a response-header timeout", err)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("took %v to give up, want roughly the header timeout", elapsed)
	}
}

// The absence of a total deadline is the point of this configuration, so pin it:
// reintroducing http.Client.Timeout would silently cap transfer size again.
func TestClientHasNoTotalDeadline(t *testing.T) {
	c := NewHTTPClient()
	if c.Timeout != 0 {
		t.Fatalf("http.Client.Timeout = %v; it covers the response body, so it caps "+
			"how large a transfer can be at a given bandwidth", c.Timeout)
	}
	tr, ok := c.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport is %T, want *http.Transport", c.Transport)
	}
	if tr.ResponseHeaderTimeout != HeaderTimeout {
		t.Errorf("ResponseHeaderTimeout = %v, want %v", tr.ResponseHeaderTimeout, HeaderTimeout)
	}
	if tr.TLSHandshakeTimeout != TLSTimeout {
		t.Errorf("TLSHandshakeTimeout = %v, want %v", tr.TLSHandshakeTimeout, TLSTimeout)
	}
	if tr.Proxy == nil {
		t.Error("Proxy is nil; HTTPS_PROXY must keep working")
	}
}

// New() must hand its client the same configuration.
func TestNewUsesTheSharedClient(t *testing.T) {
	c := New("https://example.test", "ask_x", "ua")
	if c.HTTP == nil || c.HTTP.Timeout != 0 {
		t.Fatalf("client HTTP = %+v, want the shared no-total-deadline client", c.HTTP)
	}
}
