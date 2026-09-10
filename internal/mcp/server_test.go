package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/aispace-sh/aispace-client/internal/api"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

func testServer(t *testing.T) (*sdkmcp.Server, *Service) {
	t.Helper()
	root := t.TempDir()
	t.Setenv(envAllowedRoots, root)
	roots, err := newRootsPolicy()
	if err != nil {
		t.Fatal(err)
	}
	s := &Service{client: api.New("http://127.0.0.1", "ask_test_secret", "test"), roots: roots}
	return s.server("1.2.3", io.Discard), s
}

func connect(t *testing.T, server *sdkmcp.Server, client *sdkmcp.Client) *sdkmcp.ClientSession {
	t.Helper()
	ctx := context.Background()
	st, ct := sdkmcp.NewInMemoryTransports()
	if _, err := server.Connect(ctx, st, nil); err != nil {
		t.Fatal(err)
	}
	session, err := client.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}

func TestToolCatalogAndInstructions(t *testing.T) {
	server, _ := testServer(t)
	client := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "test", Version: "1"}, nil)
	session := connect(t, server, client)
	if got := session.InitializeResult().Instructions; got != Instructions {
		t.Fatalf("instructions mismatch: %q", got)
	}
	if caps := session.InitializeResult().Capabilities; caps.Tools == nil || caps.Logging != nil || caps.Resources != nil || caps.Prompts != nil {
		t.Fatalf("capabilities = %#v; want tools only", caps)
	}
	res, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"aispace_whoami", "aispace_get_quota", "aispace_list_files", "aispace_get_file", "aispace_upload_file", "aispace_download_file", "aispace_create_link", "aispace_list_links", "aispace_revoke_link", "aispace_delete_file"}
	byName := map[string]*sdkmcp.Tool{}
	for _, tool := range res.Tools {
		byName[tool.Name] = tool
	}
	if len(byName) != len(want) {
		t.Fatalf("tool count = %d, want %d", len(byName), len(want))
	}
	for _, name := range want {
		if byName[name] == nil {
			t.Errorf("missing tool %s", name)
		}
	}
	for _, name := range want {
		tool := byName[name]
		schema, _ := json.Marshal(tool.InputSchema)
		if !bytes.Contains(schema, []byte(`"additionalProperties":false`)) || !bytes.Contains(schema, []byte(`"required"`)) {
			t.Errorf("%s schema = %s", name, schema)
		}
		if tool.OutputSchema != nil {
			t.Errorf("%s unexpectedly advertises success-only output schema", name)
		}
		if tool.Annotations == nil || tool.Annotations.OpenWorldHint == nil || !*tool.Annotations.OpenWorldHint {
			t.Errorf("%s annotations incomplete", name)
		}
	}
	checks := map[string]struct{ read, destructive, idempotent bool }{
		"aispace_whoami": {true, false, true}, "aispace_get_quota": {true, false, true}, "aispace_list_files": {true, false, true}, "aispace_get_file": {true, false, true},
		"aispace_upload_file": {false, false, false}, "aispace_download_file": {false, true, false}, "aispace_create_link": {false, false, false}, "aispace_list_links": {true, false, true},
		"aispace_revoke_link": {false, true, true}, "aispace_delete_file": {false, true, false},
	}
	for name, want := range checks {
		a := byName[name].Annotations
		if a.ReadOnlyHint != want.read || a.DestructiveHint == nil || *a.DestructiveHint != want.destructive || a.IdempotentHint != want.idempotent {
			t.Errorf("%s annotations = %#v", name, a)
		}
	}
}

func TestInvalidArgumentsAreStructuredToolErrors(t *testing.T) {
	server, _ := testServer(t)
	session := connect(t, server, sdkmcp.NewClient(&sdkmcp.Implementation{Name: "test", Version: "1"}, nil))
	res, err := session.CallTool(context.Background(), &sdkmcp.CallToolParams{Name: "aispace_list_files", Arguments: map[string]any{"limit": 0, "unknown": true}})
	if err != nil {
		t.Fatalf("protocol error: %v", err)
	}
	if !res.IsError {
		t.Fatal("invalid arguments did not produce a tool error")
	}
	raw, _ := json.Marshal(res.StructuredContent)
	if !bytes.Contains(raw, []byte(`"category":"input"`)) || !bytes.Contains(raw, []byte(`"ok":false`)) {
		t.Fatalf("structured error = %s", raw)
	}
	if bytes.Contains(raw, []byte("ask_test_secret")) {
		t.Fatal("credential leaked")
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func response(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}

func TestUploadUsesSafeDefaultsAndIdempotency(t *testing.T) {
	server, service := testServer(t)
	root := service.roots.explicit[0]
	path := filepath.Join(root, "report.txt")
	if err := os.WriteFile(path, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var seen []string
	service.client.HTTP = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		mu.Lock()
		defer mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer ask_test_secret" {
			t.Errorf("authorization = %q", r.Header.Get("Authorization"))
		}
		switch r.URL.Path {
		case "/v1/quota":
			return response(200, `{"key":{"budget_bytes":1000,"used_bytes":0,"remaining_bytes":1000,"budget_limited":true},"account":{"allowance_bytes":1000,"used_bytes":0,"remaining_bytes":1000,"plan":"pro","extra_blocks":0},"month":{"uploads_used":0,"uploads_limit":10,"downloads_used":0,"downloads_limit":10,"period_end":1},"limits":{"max_file_bytes":1000,"max_file_ttl_seconds":1000,"max_link_ttl_seconds":1000,"uploads_per_hour":10,"uploads_per_day":10,"requests_per_minute":10},"rate":{"uploads_hour_remaining":10,"uploads_day_remaining":10,"requests_minute_remaining":10}}`), nil
		case "/v1/files":
			body, _ := io.ReadAll(r.Body)
			seen = []string{r.Header.Get("X-File-Visibility"), r.Header.Get("X-SHA256"), r.Header.Get("Idempotency-Key"), string(body)}
			return response(201, `{"id":"file1","name":"report.txt","content_type":"text/plain; charset=utf-8","size_bytes":5,"sha256":"2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824","enc_alg":null,"visibility":"private","created_at":1,"expires_at":2}`), nil
		default:
			t.Fatalf("unexpected request %s", r.URL.Path)
			return nil, nil
		}
	})}
	client := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "test", Version: "1"}, nil)
	client.AddRoots(&sdkmcp.Root{URI: "file://" + filepath.ToSlash(root)})
	session := connect(t, server, client)
	res, err := session.CallTool(context.Background(), &sdkmcp.CallToolParams{Name: "aispace_upload_file", Arguments: map[string]any{"path": path}})
	if err != nil || res.IsError {
		t.Fatalf("upload result=%#v err=%v", res, err)
	}
	if len(seen) != 4 || seen[0] != "private" || len(seen[1]) != 64 || seen[2] == "" || seen[3] != "hello" {
		t.Fatalf("upload headers/body = %#v", seen)
	}
	raw, _ := json.Marshal(res)
	if bytes.Contains(raw, []byte("ask_test_secret")) || bytes.Contains(raw, []byte(path)) {
		t.Fatalf("result leaked secret/path: %s", raw)
	}
}

func TestPathPolicyRejectsSymlinkEscape(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	t.Setenv(envAllowedRoots, root)
	p, err := newRootsPolicy()
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(outside, "secret")
	if err := os.WriteFile(target, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "escape")
	if err := os.Symlink(target, link); err != nil {
		t.Skip(err)
	}
	if _, _, err := p.uploadPath(link); err == nil {
		t.Fatal("symlink escape accepted")
	}
}

func TestErrorMappingAndRedaction(t *testing.T) {
	cases := []struct {
		code, category string
		retryAfter     int
		retryable      bool
	}{
		{"bad_request", "input", 0, false}, {"invalid_key", "auth", 0, false},
		{"forbidden", "permission", 0, false}, {"not_found", "not_found", 0, false},
		{"monthly_upload_cap", "quota", 0, false}, {"rate_limited", "rate_limit", 9, true},
		{"conflict", "conflict", 0, false}, {"conflict", "conflict", 2, true},
		{"internal", "service", 0, false}, {"internal", "service", 3, true},
	}
	for _, tc := range cases {
		t.Run(tc.code, func(t *testing.T) {
			e := mapError(&api.Error{Code: tc.code, Message: "failure", RetryAfter: tc.retryAfter, RequestID: "req-1"}, "service", "fallback")
			if e.Category != tc.category || e.Retryable != tc.retryable || e.RequestID != "req-1" {
				t.Fatalf("mapped = %#v", e)
			}
		})
	}
	message := safeMessage(errors.New("Bearer ask_supersecret at https://aispace.sh/d/bearer-token?q=secret#fragment"))
	for _, secret := range []string{"ask_supersecret", "bearer-token", "q=secret", "fragment"} {
		if strings.Contains(message, secret) {
			t.Errorf("redaction leaked %q in %q", secret, message)
		}
	}
}

func TestDownloadVerifiesBeforePublishing(t *testing.T) {
	server, service := testServer(t)
	root := service.roots.explicit[0]
	dest := filepath.Join(root, "download.txt")
	digest := "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824"
	var contentCalls int
	service.client.HTTP = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch r.URL.Path {
		case "/v1/files/file1":
			return response(200, `{"id":"file1","name":"download.txt","content_type":"text/plain","size_bytes":5,"sha256":"`+digest+`","enc_alg":null,"visibility":"private","created_at":1,"expires_at":2}`), nil
		case "/v1/files/file1/content":
			contentCalls++
			if r.Header.Get("Idempotency-Key") == "" {
				t.Error("download has no idempotency key")
			}
			return response(200, "hello"), nil
		default:
			t.Fatalf("unexpected request %s", r.URL.Path)
			return nil, nil
		}
	})}
	client := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "test", Version: "1"}, nil)
	client.AddRoots(&sdkmcp.Root{URI: "file://" + filepath.ToSlash(root)})
	session := connect(t, server, client)
	res, err := session.CallTool(context.Background(), &sdkmcp.CallToolParams{Name: "aispace_download_file", Arguments: map[string]any{"file_id": "file1", "output_path": dest}})
	if err != nil || res.IsError {
		t.Fatalf("download result=%#v err=%v", res, err)
	}
	got, err := os.ReadFile(dest)
	if err != nil || string(got) != "hello" {
		t.Fatalf("downloaded %q, err=%v", got, err)
	}
	if contentCalls != 1 {
		t.Fatalf("content calls = %d", contentCalls)
	}
	if runtime.GOOS != "windows" {
		st, _ := os.Stat(dest)
		if st.Mode().Perm() != 0o600 {
			t.Fatalf("mode = %o", st.Mode().Perm())
		}
	}
	res, err = session.CallTool(context.Background(), &sdkmcp.CallToolParams{Name: "aispace_download_file", Arguments: map[string]any{"file_id": "file1", "output_path": dest}})
	if err != nil || !res.IsError {
		t.Fatalf("overwrite refusal result=%#v err=%v", res, err)
	}
	if contentCalls != 1 {
		t.Fatalf("existing destination caused a download; calls=%d", contentCalls)
	}
}
