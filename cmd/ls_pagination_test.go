package cmd

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/aispace-sh/aispace-client/internal/config"
)

// pagedServer serves total files through cursor pagination, honouring the
// limit query parameter and capping each page at pageMax the way a real API
// would.
type pagedServer struct {
	mu       sync.Mutex
	srv      *httptest.Server
	key      string
	total    int
	pageMax  int
	requests []string
}

func newPagedServer(t *testing.T, total, pageMax int) *pagedServer {
	t.Helper()
	p := &pagedServer{key: "ask_validkey0000000000000000000000", total: total, pageMax: pageMax}
	p.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+p.key {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":{"code":"invalid_key","message":"Invalid key"}}`))
			return
		}
		q := r.URL.Query()
		p.mu.Lock()
		p.requests = append(p.requests, "cursor="+q.Get("cursor")+" limit="+q.Get("limit"))
		p.mu.Unlock()

		start := 0
		if cur := q.Get("cursor"); cur != "" {
			start, _ = strconv.Atoi(cur)
		}
		size := p.pageMax
		if l := q.Get("limit"); l != "" {
			n, err := strconv.Atoi(l)
			if err != nil || n < 1 || n > 100 {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"error":{"code":"bad_request","message":"limit must be between 1 and 100"}}`))
				return
			}
			if n < size {
				size = n
			}
		}
		end := start + size
		if end > p.total {
			end = p.total
		}
		var items []string
		for i := start; i < end; i++ {
			items = append(items, fmt.Sprintf(`{"id":"F%03d","name":"f%03d.txt","content_type":"text/plain","size_bytes":%d,"visibility":"account","created_at":1,"expires_at":1757604800}`, i, i, i+1))
		}
		next := "null"
		if end < p.total {
			next = strconv.Quote(strconv.Itoa(end))
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"files":[%s],"next_cursor":%s}`, strings.Join(items, ","), next)
	}))
	t.Cleanup(p.srv.Close)
	return p
}

func (p *pagedServer) calls() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.requests...)
}

type lsJSON struct {
	Files []struct {
		ID string `json:"id"`
	} `json:"files"`
	NextCursor *string `json:"next_cursor"`
}

func parseLs(t *testing.T, out string) lsJSON {
	t.Helper()
	var got lsJSON
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("stdout %q: %v", out, err)
	}
	return got
}

// --limit must stop early rather than walking the whole account, which is the
// point: on a large account the default costs one request per page.
func TestLsLimitStopsEarly(t *testing.T) {
	isolate(t)
	p := newPagedServer(t, 100, 10)
	writeConfig(t, config.File{Key: p.key, URL: p.srv.URL})

	r := run("", "ls", "--limit", "25", "--json")
	if r.code != ExitOK {
		t.Fatalf("%+v", r)
	}
	got := parseLs(t, r.stdout)
	if len(got.Files) != 25 {
		t.Fatalf("got %d files, want 25", len(got.Files))
	}
	if got.NextCursor == nil || *got.NextCursor != "25" {
		t.Fatalf("next_cursor = %v, want a resume point at 25", got.NextCursor)
	}
	// 25 files at 10 per page is three requests, not the ten the full walk costs.
	if n := len(p.calls()); n != 3 {
		t.Fatalf("made %d requests (%v), want 3", n, p.calls())
	}
}

// The cursor handed back must resume exactly where the previous page stopped,
// with no gap and no repeat.
func TestLsCursorResumesWithoutGapOrOverlap(t *testing.T) {
	isolate(t)
	p := newPagedServer(t, 30, 7)
	writeConfig(t, config.File{Key: p.key, URL: p.srv.URL})

	var seen []string
	cursor := ""
	for page := 0; page < 10; page++ {
		args := []string{"ls", "--limit", "8", "--json"}
		if cursor != "" {
			args = append(args, "--cursor", cursor)
		}
		r := run("", args...)
		if r.code != ExitOK {
			t.Fatalf("page %d: %+v", page, r)
		}
		got := parseLs(t, r.stdout)
		for _, f := range got.Files {
			seen = append(seen, f.ID)
		}
		if got.NextCursor == nil {
			break
		}
		cursor = *got.NextCursor
	}
	if len(seen) != 30 {
		t.Fatalf("collected %d ids, want 30: %v", len(seen), seen)
	}
	for i, id := range seen {
		if want := fmt.Sprintf("F%03d", i); id != want {
			t.Fatalf("id[%d] = %s, want %s (order, gap or overlap)", i, id, want)
		}
	}
}

// A limit larger than the account must end cleanly rather than reporting a
// resume point that leads nowhere.
func TestLsLimitBeyondEndReportsNoCursor(t *testing.T) {
	isolate(t)
	p := newPagedServer(t, 12, 5)
	writeConfig(t, config.File{Key: p.key, URL: p.srv.URL})

	r := run("", "ls", "--limit", "100", "--json")
	if r.code != ExitOK {
		t.Fatalf("%+v", r)
	}
	got := parseLs(t, r.stdout)
	if len(got.Files) != 12 {
		t.Fatalf("got %d files, want 12", len(got.Files))
	}
	if got.NextCursor != nil {
		t.Fatalf("next_cursor = %v, want null at the end of the listing", *got.NextCursor)
	}
}

func TestLsLimitAboveServerMaximumUsesMultipleRequests(t *testing.T) {
	isolate(t)
	p := newPagedServer(t, 300, 100)
	writeConfig(t, config.File{Key: p.key, URL: p.srv.URL})

	r := run("", "ls", "--limit", "250", "--json")
	if r.code != ExitOK {
		t.Fatalf("%+v", r)
	}
	got := parseLs(t, r.stdout)
	if len(got.Files) != 250 {
		t.Fatalf("got %d files, want 250", len(got.Files))
	}
	if got.NextCursor == nil || *got.NextCursor != "250" {
		t.Fatalf("next_cursor = %v, want a resume point at 250", got.NextCursor)
	}
	wantCalls := []string{"cursor= limit=100", "cursor=100 limit=100", "cursor=200 limit=50"}
	if calls := p.calls(); !slices.Equal(calls, wantCalls) {
		t.Fatalf("calls = %v, want %v", calls, wantCalls)
	}
}

// Without --limit nothing changes: every page is walked and next_cursor is null.
func TestLsWithoutLimitStillWalksEverything(t *testing.T) {
	isolate(t)
	p := newPagedServer(t, 23, 10)
	writeConfig(t, config.File{Key: p.key, URL: p.srv.URL})

	r := run("", "ls", "--json")
	if r.code != ExitOK {
		t.Fatalf("%+v", r)
	}
	got := parseLs(t, r.stdout)
	if len(got.Files) != 23 || got.NextCursor != nil {
		t.Fatalf("files=%d next_cursor=%v, want 23 and null", len(got.Files), got.NextCursor)
	}
}

// Human mode keeps stdout one line per file and puts the resume hint on stderr.
func TestLsLimitHumanOutputKeepsStdoutClean(t *testing.T) {
	isolate(t)
	p := newPagedServer(t, 50, 10)
	writeConfig(t, config.File{Key: p.key, URL: p.srv.URL})

	r := run("", "ls", "--limit", "3")
	if r.code != ExitOK {
		t.Fatalf("%+v", r)
	}
	lines := strings.Split(strings.TrimSpace(r.stdout), "\n")
	if len(lines) != 3 {
		t.Fatalf("stdout has %d lines, want 3: %q", len(lines), r.stdout)
	}
	if !strings.HasPrefix(lines[0], "F000 ") {
		t.Fatalf("first line = %q", lines[0])
	}
	if !strings.Contains(r.stderr, "--limit 3 --cursor 3") {
		t.Fatalf("stderr should preserve the page size and name the resume cursor: %q", r.stderr)
	}
}

func TestLsRejectsNegativeLimit(t *testing.T) {
	isolate(t)
	p := newPagedServer(t, 1, 1)
	writeConfig(t, config.File{Key: p.key, URL: p.srv.URL})

	r := run("", "ls", "--limit", "-1")
	if r.code != ExitUsage || !strings.Contains(r.stderr, "--limit must be >= 1") {
		t.Fatalf("%+v", r)
	}
}

func TestLsRejectsExplicitZeroLimit(t *testing.T) {
	isolate(t)
	p := newPagedServer(t, 1, 1)
	writeConfig(t, config.File{Key: p.key, URL: p.srv.URL})

	r := run("", "ls", "--limit", "0")
	if r.code != ExitUsage || !strings.Contains(r.stderr, "--limit must be >= 1") {
		t.Fatalf("%+v", r)
	}
	if calls := p.calls(); len(calls) != 0 {
		t.Fatalf("server received requests for invalid limit: %v", calls)
	}
}
