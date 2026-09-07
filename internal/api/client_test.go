package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

func newTestClient(t testing.TB, h http.HandlerFunc) (*Client, *[]time.Duration) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c := New(srv.URL+"/", "ask_testkey", "aispace-cli/test (test/test)")
	var slept []time.Duration
	c.Sleep = func(d time.Duration) { slept = append(slept, d) }
	return c, &slept
}

func TestUploadHeadersAndStreaming(t *testing.T) {
	var got *http.Request
	var body []byte
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		got = r
		body, _ = io.ReadAll(r.Body)
		w.WriteHeader(201)
		_, _ = w.Write([]byte(`{"id":"01F","name":"a.txt","content_type":"text/plain","size_bytes":5,"created_at":1,"expires_at":2}`))
	})
	res, err := c.Upload(context.Background(), strings.NewReader("hello"), UploadOptions{
		Name: "a.txt", ContentType: "text/plain", Encryption: "age-x25519", Size: 5, ExpiresIn: 3600, SHA256: "abc", Visibility: "account",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Method != http.MethodPost || got.URL.Path != "/v1/files" {
		t.Fatalf("bad request %s %s", got.Method, got.URL.Path)
	}
	if got.ContentLength != 5 || string(body) != "hello" {
		t.Fatalf("content-length %d body %q", got.ContentLength, body)
	}
	h := got.Header
	if h.Get("Authorization") != "Bearer ask_testkey" ||
		h.Get("X-File-Name") != "a.txt" ||
		h.Get("Content-Type") != "text/plain" ||
		h.Get("X-Expires-In") != "3600" ||
		h.Get("X-SHA256") != "abc" ||
		h.Get("X-Aispace-Encryption") != "age-x25519" ||
		h.Get("X-File-Visibility") != "account" ||
		h.Get("User-Agent") != "aispace-cli/test (test/test)" {
		t.Fatalf("headers: %v", h)
	}
	if res.Value.ID != "01F" || res.Value.SizeBytes != 5 {
		t.Fatalf("decoded %+v", res.Value)
	}
	if !strings.HasPrefix(string(res.Raw), `{"id":"01F"`) {
		t.Fatalf("raw not preserved: %s", res.Raw)
	}
}

func TestUploadOmitsOptionalHeaders(t *testing.T) {
	var got http.Header
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		got = r.Header
		w.WriteHeader(201)
		_, _ = w.Write([]byte(`{"id":"x"}`))
	})
	if _, err := c.Upload(context.Background(), strings.NewReader(""), UploadOptions{Name: "n", Size: 0}); err != nil {
		t.Fatal(err)
	}
	if _, ok := got["X-Expires-In"]; ok {
		t.Fatal("X-Expires-In should be omitted")
	}
	if _, ok := got["X-Sha256"]; ok {
		t.Fatal("X-SHA256 should be omitted")
	}
	if _, ok := got["X-Aispace-Encryption"]; ok {
		t.Fatal("X-Aispace-Encryption should be omitted")
	}
	if _, ok := got["X-File-Visibility"]; ok {
		t.Fatal("X-File-Visibility should be omitted so the account setting applies")
	}
	if got.Get("Content-Type") != "application/octet-stream" {
		t.Fatalf("default content-type = %q", got.Get("Content-Type"))
	}
}

func TestDownloadAuthenticatedContent(t *testing.T) {
	var got *http.Request
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		got = r
		w.Header().Set("Content-Disposition", `attachment; filename="shared.txt"`)
		_, _ = w.Write([]byte("shared contents"))
	})
	resp, err := c.Download(context.Background(), "f/1")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if got.URL.EscapedPath() != "/v1/files/f%2F1/content" || got.Header.Get("Authorization") != "Bearer ask_testkey" || string(body) != "shared contents" {
		t.Fatalf("request=%v body=%q", got, body)
	}
}

func TestErrorDecodingAndExitCodes(t *testing.T) {
	cases := []struct {
		status   int
		body     string
		wantCode string
		wantExit int
		wantMsg  string
	}{
		{401, `{"error":{"code":"invalid_key","message":"Invalid key"}}`, "invalid_key", 3, "Invalid key"},
		{401, ``, "unauthenticated", 3, "Unauthorized (HTTP 401)"},
		{402, `{"error":{"code":"quota_exceeded","message":"Key budget exceeded","details":{"remaining":0}}}`, "quota_exceeded", 4, "Key budget exceeded"},
		{413, `{"error":{"code":"file_too_large","message":"too big"}}`, "file_too_large", 4, "too big"},
		{429, `{"error":{"code":"rate_limited","message":"slow down"}}`, "rate_limited", 5, "slow down"},
		{404, `{"error":{"code":"not_found","message":"nope"}}`, "not_found", 1, "nope"},
		{400, `{"error":{"code":"checksum_mismatch","message":"bad sum"}}`, "checksum_mismatch", 1, "bad sum"},
		{500, `<html>boom</html>`, "internal", 1, "Internal Server Error (HTTP 500)"},
	}
	for _, tc := range cases {
		c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(tc.status)
			_, _ = w.Write([]byte(tc.body))
		})
		// POST so 429 is not retried here.
		_, err := c.CreateLink(context.Background(), "f1", LinkOptions{})
		var ae *Error
		if !errors.As(err, &ae) {
			t.Fatalf("status %d: expected *Error, got %v", tc.status, err)
		}
		if ae.Status != tc.status || ae.Code != tc.wantCode || ae.ExitCode() != tc.wantExit || ae.Message != tc.wantMsg {
			t.Errorf("status %d: got status=%d code=%q exit=%d msg=%q", tc.status, ae.Status, ae.Code, ae.ExitCode(), ae.Message)
		}
		if tc.status == 402 && string(ae.Details) != `{"remaining":0}` {
			t.Errorf("details not preserved: %s", ae.Details)
		}
	}
}

func TestRateLimitRetryOnGET(t *testing.T) {
	calls := 0
	c, slept := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			w.Header().Set("Retry-After", "2")
			w.WriteHeader(429)
			_, _ = w.Write([]byte(`{"error":{"code":"rate_limited","message":"slow"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"key":{"budget_bytes":10,"used_bytes":1,"remaining_bytes":9}}`))
	})
	res, err := c.Quota(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("calls = %d, want 2", calls)
	}
	if len(*slept) != 1 || (*slept)[0] != 2*time.Second {
		t.Fatalf("slept = %v, want [2s]", *slept)
	}
	if res.Value.Key.RemainingBytes != 9 {
		t.Fatalf("decoded %+v", res.Value)
	}
}

func TestMonthlyCapIsNotRetriedOnGET(t *testing.T) {
	calls := 0
	c, slept := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Retry-After", "100")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"code":"monthly_download_cap","message":"monthly cap reached"}}`))
	})
	_, err := c.Quota(context.Background())
	var ae *Error
	if !errors.As(err, &ae) || ae.Code != "monthly_download_cap" {
		t.Fatalf("err = %v", err)
	}
	if calls != 1 || len(*slept) != 0 {
		t.Fatalf("calls=%d slept=%v, want no retry", calls, *slept)
	}
}

func TestDownloadRetriesRateLimitButNotMonthlyCap(t *testing.T) {
	t.Run("rate limited", func(t *testing.T) {
		calls := 0
		c, slept := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			calls++
			if calls == 1 {
				w.Header().Set("Retry-After", "2")
				w.WriteHeader(http.StatusTooManyRequests)
				_, _ = w.Write([]byte(`{"error":{"code":"rate_limited","message":"slow"}}`))
				return
			}
			_, _ = w.Write([]byte("contents"))
		})
		resp, err := c.Download(context.Background(), "f1")
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if calls != 2 || len(*slept) != 1 || (*slept)[0] != 2*time.Second {
			t.Fatalf("calls=%d slept=%v", calls, *slept)
		}
	})

	t.Run("monthly cap", func(t *testing.T) {
		calls := 0
		c, slept := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			calls++
			w.Header().Set("Retry-After", "100")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":{"code":"monthly_download_cap","message":"monthly cap reached"}}`))
		})
		_, err := c.Download(context.Background(), "f1")
		var ae *Error
		if !errors.As(err, &ae) || ae.Code != "monthly_download_cap" {
			t.Fatalf("err = %v", err)
		}
		if calls != 1 || len(*slept) != 0 {
			t.Fatalf("calls=%d slept=%v, want no retry", calls, *slept)
		}
	})
}

func TestRateLimitRetryOnlyOnce(t *testing.T) {
	calls := 0
	c, slept := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Retry-After", "100")
		w.WriteHeader(429)
		_, _ = w.Write([]byte(`{"error":{"code":"rate_limited","message":"slow"}}`))
	})
	_, err := c.Quota(context.Background())
	var ae *Error
	if !errors.As(err, &ae) || ae.ExitCode() != 5 || ae.RetryAfter != 100 {
		t.Fatalf("err = %v", err)
	}
	if calls != 2 {
		t.Fatalf("calls = %d, want exactly 2 (one retry)", calls)
	}
	if len(*slept) != 1 || (*slept)[0] != MaxRetryAfter {
		t.Fatalf("slept = %v, want capped at %v", *slept, MaxRetryAfter)
	}
}

func TestRateLimitRetryStopsWhenContextCanceledDuringWait(t *testing.T) {
	calls := 0
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Retry-After", "30")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"code":"rate_limited","message":"slow"}}`))
	})
	waiting := make(chan struct{})
	release := make(chan struct{})
	c.Sleep = func(time.Duration) {
		close(waiting)
		<-release
	}
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, err := c.Quota(ctx)
		result <- err
	}()
	<-waiting
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context canceled", err)
		}
	case <-time.After(250 * time.Millisecond):
		t.Fatal("request did not return promptly after cancellation")
	}
	close(release)
	if calls != 1 {
		t.Fatalf("calls = %d, want 1 (no retry after cancellation)", calls)
	}
}

func TestDefaultRetryWaitStopsWhenContextCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		result <- waitForRetry(ctx, MaxRetryAfter, nil)
	}()
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context canceled", err)
		}
	case <-time.After(250 * time.Millisecond):
		t.Fatal("default wait did not return promptly after cancellation")
	}
}

func TestRateLimitNoRetryOnNonGET(t *testing.T) {
	calls := 0
	c, slept := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Retry-After", "1")
		w.WriteHeader(429)
		_, _ = w.Write([]byte(`{"error":{"code":"rate_limited","message":"slow"}}`))
	})
	for _, call := range []func() error{
		func() error {
			_, err := c.Upload(context.Background(), strings.NewReader("x"), UploadOptions{Size: 1})
			return err
		},
		func() error { _, err := c.CreateLink(context.Background(), "f", LinkOptions{}); return err },
		func() error { return c.DeleteFile(context.Background(), "f") },
		func() error { return c.RevokeLink(context.Background(), "l") },
	} {
		calls = 0
		err := call()
		var ae *Error
		if !errors.As(err, &ae) || ae.ExitCode() != 5 {
			t.Fatalf("err = %v", err)
		}
		if calls != 1 {
			t.Fatalf("calls = %d, want 1 (no retry for non-GET)", calls)
		}
	}
	if len(*slept) != 0 {
		t.Fatalf("should not sleep for non-GET: %v", *slept)
	}
}

func TestRetryAfterParsing(t *testing.T) {
	if retryDelay("") != DefaultRetryAfter {
		t.Fatal("missing header should use default")
	}
	if retryDelay("garbage") != DefaultRetryAfter {
		t.Fatal("garbage should use default")
	}
	if retryDelay("5") != 5*time.Second {
		t.Fatal("seconds")
	}
	if retryDelay("999") != MaxRetryAfter {
		t.Fatal("cap")
	}
	if retryDelay("-3") != 0 {
		t.Fatal("negative clamps to zero")
	}
	future := time.Now().Add(10 * time.Second).UTC().Format(http.TimeFormat)
	if d := retryDelay(future); d < 8*time.Second || d > 11*time.Second {
		t.Fatalf("http-date = %v", d)
	}
	past := time.Now().Add(-10 * time.Second).UTC().Format(http.TimeFormat)
	if retryDelay(past) != 0 {
		t.Fatal("past date should be zero")
	}
}

func TestListAllFilesPaginates(t *testing.T) {
	var cursors []string
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/files" || r.Method != http.MethodGet {
			t.Errorf("bad request %s %s", r.Method, r.URL.Path)
		}
		cur := r.URL.Query().Get("cursor")
		cursors = append(cursors, cur)
		switch cur {
		case "":
			_, _ = w.Write([]byte(`{"files":[{"id":"a","name":"a.txt","size_bytes":1}],"next_cursor":"c2"}`))
		case "c2":
			_, _ = w.Write([]byte(`{"files":[{"id":"b","name":"b.txt","size_bytes":2}],"next_cursor":"c3"}`))
		case "c3":
			_, _ = w.Write([]byte(`{"files":[],"next_cursor":null}`))
		default:
			t.Errorf("unexpected cursor %q", cur)
		}
	})
	raws, files, err := c.ListAllFiles(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 || files[0].ID != "a" || files[1].ID != "b" {
		t.Fatalf("files = %+v", files)
	}
	if len(raws) != 2 || string(raws[1]) != `{"id":"b","name":"b.txt","size_bytes":2}` {
		t.Fatalf("raws = %s", raws)
	}
	if strings.Join(cursors, ",") != ",c2,c3" {
		t.Fatalf("cursors = %v", cursors)
	}
}

func TestListAllFilesDetectsLoop(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"files":[],"next_cursor":"same"}`))
	})
	if _, _, err := c.ListAllFiles(context.Background()); err == nil {
		t.Fatal("expected pagination loop error")
	}
}

func TestWalkFilesVisitsPageBeforeFetchingNext(t *testing.T) {
	secondRequested := make(chan struct{})
	releaseSecond := make(chan struct{})
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("cursor") == "" {
			_, _ = w.Write([]byte(`{"files":[{"id":"a"}],"next_cursor":"next"}`))
			return
		}
		close(secondRequested)
		<-releaseSecond
		_, _ = w.Write([]byte(`{"files":[{"id":"b"}],"next_cursor":null}`))
	})
	firstVisited := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		visits := 0
		done <- c.WalkFiles(context.Background(), func(_ []json.RawMessage, files []File) error {
			visits++
			if visits == 1 {
				if len(files) != 1 || files[0].ID != "a" {
					t.Errorf("first page = %+v", files)
				}
				close(firstVisited)
			}
			return nil
		})
	}()
	select {
	case <-firstVisited:
	case <-time.After(time.Second):
		t.Fatal("first page was not visited")
	}
	select {
	case <-secondRequested:
	case <-time.After(time.Second):
		t.Fatal("second page was not requested")
	}
	close(releaseSecond)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestWalkFilesStopsAfterCanceledVisit(t *testing.T) {
	calls := 0
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		_, _ = w.Write([]byte(`{"files":[{"id":"a"}],"next_cursor":"next"}`))
	})
	want := context.Canceled
	err := c.WalkFiles(context.Background(), func(_ []json.RawMessage, _ []File) error { return want })
	if !errors.Is(err, want) || calls != 1 {
		t.Fatalf("err = %v, calls = %d", err, calls)
	}
}

func BenchmarkWalkFiles100k(b *testing.B) {
	const pages = 1000
	entries := strings.Repeat(`{"id":"f"},`, 99) + `{"id":"f"}`
	c, _ := newTestClient(b, func(w http.ResponseWriter, r *http.Request) {
		page, _ := strconv.Atoi(r.URL.Query().Get("cursor"))
		if page+1 < pages {
			fmt.Fprintf(w, `{"files":[%s],"next_cursor":"%d"}`, entries, page+1)
			return
		}
		fmt.Fprintf(w, `{"files":[%s],"next_cursor":null}`, entries)
	})
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		count := 0
		err := c.WalkFiles(context.Background(), func(_ []json.RawMessage, files []File) error {
			count += len(files)
			return nil
		})
		if err != nil || count != 100000 {
			b.Fatalf("count = %d, err = %v", count, err)
		}
	}
}

func TestDeleteAndRevoke(t *testing.T) {
	var paths []string
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.Method+" "+r.URL.EscapedPath())
		w.WriteHeader(204)
	})
	if err := c.DeleteFile(context.Background(), "f/1"); err != nil {
		t.Fatal(err)
	}
	if err := c.RevokeLink(context.Background(), "l1"); err != nil {
		t.Fatal(err)
	}
	if paths[0] != "DELETE /v1/files/f%2F1" || paths[1] != "DELETE /v1/links/l1" {
		t.Fatalf("paths = %v", paths)
	}
}

func TestCreateLinkBody(t *testing.T) {
	var body map[string]any
	var ct string
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		ct = r.Header.Get("Content-Type")
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.WriteHeader(201)
		_, _ = w.Write([]byte(`{"id":"l1","file_id":"f1","url":"https://x/d/t","expires_at":9,"max_downloads":3}`))
	})
	res, err := c.CreateLink(context.Background(), "f1", LinkOptions{ExpiresIn: 60, MaxDownloads: 3})
	if err != nil {
		t.Fatal(err)
	}
	if ct != "application/json" || body["expires_in"] != float64(60) || body["max_downloads"] != float64(3) {
		t.Fatalf("ct=%q body=%v", ct, body)
	}
	if res.Value.URL != "https://x/d/t" || *res.Value.MaxDownloads != 3 {
		t.Fatalf("decoded %+v", res.Value)
	}
	// Empty options send an empty object.
	body = nil
	if _, err := c.CreateLink(context.Background(), "f1", LinkOptions{}); err != nil {
		t.Fatal(err)
	}
	if len(body) != 0 {
		t.Fatalf("expected empty body, got %v", body)
	}
}

func TestWhoami(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/whoami" {
			t.Errorf("path %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"key":{"id":"k","name":"bot","prefix":"ask_12345678"},"user":{"email":"a@b.c"}}`))
	})
	res, err := c.Whoami(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res.Value.Key.Name != "bot" || res.Value.User.Email != "a@b.c" {
		t.Fatalf("decoded %+v", res.Value)
	}
}

func TestMissingKeyIsAuthError(t *testing.T) {
	c := New("http://127.0.0.1:1", "", "ua")
	_, err := c.Quota(context.Background())
	var ae *Error
	if !errors.As(err, &ae) || ae.ExitCode() != 3 {
		t.Fatalf("err = %v", err)
	}
}

func TestNetworkErrorDoesNotLeakKey(t *testing.T) {
	c := New("http://127.0.0.1:1", "ask_supersecret", "ua")
	c.HTTP = &http.Client{Timeout: time.Second}
	_, err := c.Quota(context.Background())
	if err == nil {
		t.Fatal("expected error")
	}
	if strings.Contains(err.Error(), "supersecret") {
		t.Fatalf("key leaked: %v", err)
	}
	var ae *Error
	if !errors.As(err, &ae) || ae.Code != "network" || ae.ExitCode() != 1 {
		t.Fatalf("err = %#v", err)
	}
}

func TestBadJSONResponse(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`not json`))
	})
	_, err := c.Quota(context.Background())
	var ae *Error
	if !errors.As(err, &ae) || ae.Code != "bad_response" {
		t.Fatalf("err = %v", err)
	}
}
