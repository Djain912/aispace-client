// Package mcp exposes the aispace single-file API as a local MCP server.
package mcp

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/aispace-sh/aispace-client/internal/api"
	"github.com/aispace-sh/aispace-client/internal/config"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

const Instructions = `aispace stores temporary artifacts. Uploads are private unless visibility="account" is explicitly
requested. Public URLs require an explicit aispace_create_link call and a Pro account. Never upload
credentials, secret keys, environment files, auth cookies, or private-key material. Prefer short
expirations. Before large uploads call aispace_get_quota. Downloads verify SHA-256 and never
overwrite by default. Files and links expire; report the returned expiry to the user.`

type Service struct {
	client *api.Client
	roots  *rootsPolicy
}

func NewServer(version string, stderr io.Writer) (*sdkmcp.Server, error) {
	cfg, err := config.Resolve("", "")
	if err != nil {
		return nil, fmt.Errorf("load aispace configuration: %w", err)
	}
	if err := validateServiceURL(cfg.URL); err != nil {
		return nil, err
	}
	roots, err := newRootsPolicy()
	if err != nil {
		return nil, err
	}
	s := &Service{
		client: api.New(cfg.URL, cfg.Key, fmt.Sprintf("aispace/%s mcp/1 (%s/%s)", version, runtime.GOOS, runtime.GOARCH)),
		roots:  roots,
	}
	return s.server(version, stderr), nil
}

func (s *Service) server(version string, _ io.Writer) *sdkmcp.Server {
	// The SDK logger is deliberately silenced. MCP diagnostics are opt-in and
	// must be redacted before they are ever written to the host's stderr.
	logger := slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelWarn}))
	server := sdkmcp.NewServer(
		&sdkmcp.Implementation{Name: "aispace", Version: version},
		&sdkmcp.ServerOptions{
			Instructions: Instructions,
			Logger:       logger,
			Capabilities: &sdkmcp.ServerCapabilities{Tools: &sdkmcp.ToolCapabilities{}},
			InitializedHandler: func(ctx context.Context, req *sdkmcp.InitializedRequest) {
				_ = s.roots.refreshHost(ctx, req.Session)
			},
			RootsListChangedHandler: func(ctx context.Context, req *sdkmcp.RootsListChangedRequest) {
				_ = s.roots.refreshHost(ctx, req.Session)
			},
		},
	)
	s.register(server)
	return server
}

func validateServiceURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return errors.New("AISPACE_URL must be an absolute service origin without credentials, query, or fragment")
	}
	if u.Scheme == "https" {
		return nil
	}
	host := u.Hostname()
	if u.Scheme == "http" && (host == "localhost" || host == "127.0.0.1" || host == "::1") {
		return nil
	}
	return errors.New("AISPACE_URL must use HTTPS (HTTP is allowed only for an explicit loopback address)")
}

func (s *Service) register(server *sdkmcp.Server) {
	read := annotations(true, false, true)
	write := annotations(false, false, false)
	destructive := annotations(false, true, false)
	idempotentDestructive := annotations(false, true, true)

	add(server, tool("aispace_whoami", "Validate the configured credential and return non-secret key metadata.", emptySchema(), read), s.whoami)
	add(server, tool("aispace_get_quota", "Read current key, account, monthly, burst, and plan limits. Call before a large upload or batch; never hardcode plan limits.", emptySchema(), read), s.quota)
	add(server, tool("aispace_list_files", "List one bounded page of accessible files; this never auto-pages.", listSchema(), read), s.listFiles)
	add(server, tool("aispace_get_file", "Get metadata for one accessible file without downloading it.", idSchema("file_id"), read), s.getFile)
	add(server, tool("aispace_upload_file", "Upload one existing regular file within an allowed root. Uploads are private by default and never create a public link.", uploadSchema(), write), s.uploadFile)
	add(server, tool("aispace_download_file", "Download an authenticated file ID to an allowed local path, verifying SHA-256 and refusing overwrite by default.", downloadSchema(), destructive), s.downloadFile)
	add(server, tool("aispace_create_link", "Explicitly create a public bearer link for a file. Requires Pro; defaults to one hour and is capped at 24 hours.", createLinkSchema(), write), s.createLink)
	add(server, tool("aispace_list_links", "List public-link metadata for a file. Existing bearer URLs are never returned.", idSchema("file_id"), read), s.listLinks)
	add(server, tool("aispace_revoke_link", "Revoke one public link while leaving its file intact.", idSchema("link_id"), idempotentDestructive), s.revokeLink)
	add(server, tool("aispace_delete_file", "Permanently delete one owned file and revoke all links to it.", idSchema("file_id"), destructive), s.deleteFile)
}

func annotations(readOnly, destructive, idempotent bool) *sdkmcp.ToolAnnotations {
	t, f := true, false
	d := &f
	if destructive {
		d = &t
	}
	return &sdkmcp.ToolAnnotations{ReadOnlyHint: readOnly, DestructiveHint: d, IdempotentHint: idempotent, OpenWorldHint: &t}
}

func tool(name, description string, schema any, a *sdkmcp.ToolAnnotations) *sdkmcp.Tool {
	return &sdkmcp.Tool{Name: name, Description: description, InputSchema: schema, Annotations: a}
}

type rawHandler func(context.Context, *sdkmcp.CallToolRequest, json.RawMessage) (*sdkmcp.CallToolResult, any)

func add(server *sdkmcp.Server, t *sdkmcp.Tool, handler rawHandler) {
	server.AddTool(t, func(ctx context.Context, req *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
		res, out := handler(ctx, req, req.Params.Arguments)
		res.StructuredContent = out
		return res, nil
	})
}

func decode(raw json.RawMessage, dst any) error {
	if len(raw) == 0 {
		raw = json.RawMessage(`{}`)
	}
	d := json.NewDecoder(strings.NewReader(string(raw)))
	d.DisallowUnknownFields()
	if err := d.Decode(dst); err != nil {
		return errors.New("arguments do not match the tool schema")
	}
	if d.More() {
		return errors.New("arguments contain trailing JSON")
	}
	return nil
}

func hasField(raw json.RawMessage, name string) bool {
	var values map[string]json.RawMessage
	_ = json.Unmarshal(raw, &values)
	_, ok := values[name]
	return ok
}

func success(out any, text string) (*sdkmcp.CallToolResult, any) {
	return &sdkmcp.CallToolResult{Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: bounded(text, 1024)}}}, out
}

func failure(err error, category, code string) (*sdkmcp.CallToolResult, any) {
	e := mapError(err, category, code)
	return errorResult(e)
}

func failureNonRetryable(err error, category, code string) (*sdkmcp.CallToolResult, any) {
	e := mapError(err, category, code)
	if e.Category == "network" || e.Category == "service" || e.Category == "conflict" {
		e.Retryable = false
		e.RetryAfterSeconds = 0
	}
	return errorResult(e)
}

func errorResult(e ToolError) (*sdkmcp.CallToolResult, any) {
	text := fmt.Sprintf("aispace %s error (%s): %s", e.Category, e.Code, e.Message)
	return &sdkmcp.CallToolResult{IsError: true, Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: bounded(text, 1024)}}}, e
}

type EmptyInput struct{}
type UploadInput struct {
	Path         string `json:"path"`
	Name         string `json:"name"`
	ContentType  string `json:"content_type"`
	ExpiresIn    int64  `json:"expires_in"`
	Visibility   string `json:"visibility"`
	VerifySHA256 *bool  `json:"verify_sha256"`
}
type DownloadInput struct {
	FileID     string `json:"file_id"`
	OutputPath string `json:"output_path"`
	Overwrite  bool   `json:"overwrite"`
}
type CreateLinkInput struct {
	FileID       string `json:"file_id"`
	ExpiresIn    int64  `json:"expires_in"`
	MaxDownloads int64  `json:"max_downloads"`
}

type File struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	ContentType string  `json:"content_type"`
	SizeBytes   int64   `json:"size_bytes"`
	SHA256      string  `json:"sha256"`
	EncAlg      *string `json:"enc_alg"`
	Visibility  string  `json:"visibility"`
	CreatedAt   int64   `json:"created_at"`
	ExpiresAt   int64   `json:"expires_at"`
}

func fileValue(f api.File) File {
	var enc *string
	if f.EncAlg != "" {
		v := f.EncAlg
		enc = &v
	}
	return File{f.ID, f.Name, f.ContentType, f.SizeBytes, f.SHA256, enc, f.Visibility, f.CreatedAt, f.ExpiresAt}
}

func (s *Service) whoami(ctx context.Context, _ *sdkmcp.CallToolRequest, raw json.RawMessage) (*sdkmcp.CallToolResult, any) {
	var in EmptyInput
	if err := decode(raw, &in); err != nil {
		return failure(err, "input", "bad_request")
	}
	res, err := s.client.Whoami(ctx)
	if err != nil {
		return failure(err, "service", "request_failed")
	}
	return success(res.Value, "Authenticated as "+res.Value.User.Email+" with key "+res.Value.Key.Name+".")
}

func (s *Service) quota(ctx context.Context, _ *sdkmcp.CallToolRequest, raw json.RawMessage) (*sdkmcp.CallToolResult, any) {
	var in EmptyInput
	if err := decode(raw, &in); err != nil {
		return failure(err, "input", "bad_request")
	}
	res, err := s.client.Quota(ctx)
	if err != nil {
		return failure(err, "service", "request_failed")
	}
	return success(res.Value, fmt.Sprintf("Quota read: %d account bytes and %d key bytes remain.", res.Value.Account.RemainingBytes, res.Value.Key.RemainingBytes))
}

func (s *Service) listFiles(ctx context.Context, _ *sdkmcp.CallToolRequest, raw json.RawMessage) (*sdkmcp.CallToolResult, any) {
	var wire struct {
		Limit  int    `json:"limit"`
		Cursor string `json:"cursor"`
	}
	if err := decode(raw, &wire); err != nil {
		return failure(err, "input", "bad_request")
	}
	if wire.Limit == 0 && !hasField(raw, "limit") {
		wire.Limit = 50
	}
	if wire.Limit < 1 || wire.Limit > 100 || (strings.Contains(string(raw), `"cursor"`) && wire.Cursor == "") {
		return failure(errors.New("limit must be 1..100 and cursor must be non-empty when supplied"), "input", "bad_request")
	}
	res, err := s.client.ListFiles(ctx, wire.Cursor, wire.Limit)
	if err != nil {
		return failure(err, "service", "request_failed")
	}
	files := make([]File, 0, len(res.Value.Files))
	for _, item := range res.Value.Files {
		var f api.File
		if err := json.Unmarshal(item, &f); err != nil {
			return failure(errors.New("service returned invalid file metadata"), "service", "bad_response")
		}
		files = append(files, fileValue(f))
	}
	out := struct {
		Files      []File  `json:"files"`
		NextCursor *string `json:"next_cursor"`
	}{files, res.Value.NextCursor}
	return success(out, fmt.Sprintf("Listed %d file(s) in one page.", len(files)))
}

func idFrom(raw json.RawMessage, field string) (string, error) {
	var values map[string]string
	if err := decode(raw, &values); err != nil {
		return "", err
	}
	if len(values) != 1 || strings.TrimSpace(values[field]) == "" {
		return "", errors.New(field + " is required")
	}
	return values[field], nil
}

func (s *Service) getFile(ctx context.Context, _ *sdkmcp.CallToolRequest, raw json.RawMessage) (*sdkmcp.CallToolResult, any) {
	id, err := idFrom(raw, "file_id")
	if err != nil {
		return failure(err, "input", "bad_request")
	}
	res, err := s.client.GetFile(ctx, id)
	if err != nil {
		return failure(err, "service", "request_failed")
	}
	out := fileValue(res.Value)
	return success(out, fmt.Sprintf("File %s expires at %d.", out.ID, out.ExpiresAt))
}

func (s *Service) uploadFile(ctx context.Context, req *sdkmcp.CallToolRequest, raw json.RawMessage) (*sdkmcp.CallToolResult, any) {
	var in UploadInput
	if err := decode(raw, &in); err != nil {
		return failure(err, "input", "bad_request")
	}
	if in.Path == "" || len(in.Name) > 200 || (hasField(raw, "name") && in.Name == "") || len(in.ContentType) > 255 || (hasField(raw, "content_type") && in.ContentType == "") || in.ExpiresIn < 0 || (hasField(raw, "expires_in") && in.ExpiresIn == 0) || (in.Visibility != "" && in.Visibility != "private" && in.Visibility != "account") {
		return failure(errors.New("invalid upload arguments"), "input", "bad_request")
	}
	if strings.ContainsAny(in.Name+in.ContentType, "\r\n") {
		return failure(errors.New("name and content_type must not contain newlines"), "input", "bad_request")
	}
	if in.Name != "" && (filepath.Base(in.Name) != in.Name || strings.ContainsAny(in.Name, `/\\`) || in.Name == "." || in.Name == "..") {
		return failure(errors.New("name must be a plain filename, not a path"), "input", "bad_request")
	}
	path, st, err := s.roots.uploadPath(in.Path)
	if err != nil {
		return failure(errors.New("local path is invalid, inaccessible, or outside allowed roots"), "permission", "unsafe_path")
	}
	quota, err := s.client.Quota(ctx)
	if err != nil {
		return failure(err, "service", "request_failed")
	}
	if st.Size() > quota.Value.Limits.MaxFileBytes || st.Size() > quota.Value.Key.RemainingBytes || st.Size() > quota.Value.Account.RemainingBytes {
		return failure(errors.New("file size exceeds the current upload quota"), "quota", "quota_exceeded")
	}
	fh, err := os.Open(path)
	if err != nil {
		return failure(errors.New("local file could not be opened"), "permission", "unsafe_path")
	}
	defer fh.Close()
	verify := in.VerifySHA256 == nil || *in.VerifySHA256
	var digest string
	if verify {
		h := sha256.New()
		if _, err := io.Copy(h, fh); err != nil {
			return failure(errors.New("local file could not be hashed"), "input", "io")
		}
		digest = hex.EncodeToString(h.Sum(nil))
		if _, err := fh.Seek(0, io.SeekStart); err != nil {
			return failure(errors.New("local file could not be rewound"), "input", "io")
		}
	}
	name := in.Name
	if name == "" {
		name = filepath.Base(path)
	}
	ct := in.ContentType
	if ct == "" {
		ct = mime.TypeByExtension(filepath.Ext(name))
		if ct == "" {
			ct = "application/octet-stream"
		}
	}
	visibility := in.Visibility
	if visibility == "" {
		visibility = "private"
	}
	res, err := s.client.Upload(ctx, fh, api.UploadOptions{Name: name, ContentType: ct, Size: st.Size(), ExpiresIn: in.ExpiresIn, SHA256: digest, Visibility: visibility, IdempotencyKey: randomID()})
	if err != nil {
		return failureNonRetryable(err, "service", "upload_failed")
	}
	out := fileValue(res.Value)
	return success(out, fmt.Sprintf("Uploaded artifact %s; visibility=%s, expires_at=%d.", out.ID, out.Visibility, out.ExpiresAt))
}

func (s *Service) downloadFile(ctx context.Context, req *sdkmcp.CallToolRequest, raw json.RawMessage) (*sdkmcp.CallToolResult, any) {
	var in DownloadInput
	if err := decode(raw, &in); err != nil {
		return failure(err, "input", "bad_request")
	}
	if in.FileID == "" || in.OutputPath == "" {
		return failure(errors.New("file_id and output_path are required"), "input", "bad_request")
	}
	dest, err := s.roots.downloadPath(in.OutputPath)
	if err != nil {
		return failure(errors.New("output path is invalid, inaccessible, or outside allowed roots"), "permission", "unsafe_path")
	}
	if !in.Overwrite {
		if _, err := os.Lstat(dest); err == nil {
			return failure(errors.New("output file already exists; set overwrite=true explicitly to replace it"), "permission", "overwrite_refused")
		}
	}
	meta, err := s.client.GetFile(ctx, in.FileID)
	if err != nil {
		return failure(err, "service", "request_failed")
	}
	if meta.Value.SHA256 == "" {
		return failure(errors.New("file metadata has no SHA-256 digest"), "input", "missing_checksum")
	}
	resp, err := s.client.DownloadWithIdempotency(ctx, in.FileID, randomID())
	if err != nil {
		return failureNonRetryable(err, "service", "download_failed")
	}
	defer resp.Body.Close()
	tmp, err := os.CreateTemp(filepath.Dir(dest), ".aispace-download-*")
	if err != nil {
		return failure(errors.New("could not create a secure temporary output file"), "permission", "io")
	}
	tmpName := tmp.Name()
	committed := false
	defer func() {
		_ = tmp.Close()
		if !committed {
			_ = os.Remove(tmpName)
		}
	}()
	if err := tmp.Chmod(0o600); err != nil {
		return failure(errors.New("could not secure the temporary output file"), "permission", "io")
	}
	h := sha256.New()
	n, copyErr := io.Copy(io.MultiWriter(tmp, h), resp.Body)
	if copyErr != nil {
		return failureNonRetryable(copyErr, "network", "network")
	}
	got := hex.EncodeToString(h.Sum(nil))
	if n != meta.Value.SizeBytes || !strings.EqualFold(got, meta.Value.SHA256) {
		return failure(errors.New("downloaded bytes failed size or SHA-256 verification"), "input", "checksum_mismatch")
	}
	if err := tmp.Sync(); err != nil {
		return failure(errors.New("could not flush the output file"), "permission", "io")
	}
	if err := tmp.Close(); err != nil {
		return failure(errors.New("could not close the output file"), "permission", "io")
	}
	if in.Overwrite {
		if err := atomicReplace(tmpName, dest); err != nil {
			return failure(errors.New("could not atomically replace the output file"), "permission", "io")
		}
	} else {
		if err := os.Link(tmpName, dest); err != nil {
			if errors.Is(err, os.ErrExist) {
				return failure(errors.New("output file already exists; overwrite was not requested"), "permission", "overwrite_refused")
			}
			return failure(errors.New("could not atomically publish the output file"), "permission", "io")
		}
		if err := os.Remove(tmpName); err != nil {
			_ = os.Remove(dest)
			return failure(errors.New("could not finalize the output file"), "permission", "io")
		}
	}
	committed = true
	out := struct {
		FileID     string `json:"file_id"`
		OutputPath string `json:"output_path"`
		SizeBytes  int64  `json:"size_bytes"`
		SHA256     string `json:"sha256"`
		Verified   bool   `json:"verified"`
	}{in.FileID, dest, n, got, true}
	return success(out, fmt.Sprintf("Downloaded and verified file %s (%d bytes).", in.FileID, n))
}

func (s *Service) createLink(ctx context.Context, _ *sdkmcp.CallToolRequest, raw json.RawMessage) (*sdkmcp.CallToolResult, any) {
	var in CreateLinkInput
	if err := decode(raw, &in); err != nil {
		return failure(err, "input", "bad_request")
	}
	if in.FileID == "" || in.ExpiresIn < 0 || in.ExpiresIn > 86400 || (hasField(raw, "expires_in") && in.ExpiresIn == 0) || in.MaxDownloads < 0 || in.MaxDownloads > 1000 || (hasField(raw, "max_downloads") && in.MaxDownloads == 0) {
		return failure(errors.New("invalid public-link arguments"), "input", "bad_request")
	}
	if in.ExpiresIn == 0 {
		in.ExpiresIn = 3600
	}
	res, err := s.client.CreateLink(ctx, in.FileID, api.LinkOptions{ExpiresIn: in.ExpiresIn, MaxDownloads: in.MaxDownloads})
	if err != nil {
		return failureNonRetryable(err, "service", "link_failed")
	}
	return success(res.Value, fmt.Sprintf("Created public link %s; expires_at=%d. The returned URL is a bearer credential.", res.Value.ID, res.Value.ExpiresAt))
}

func (s *Service) listLinks(ctx context.Context, _ *sdkmcp.CallToolRequest, raw json.RawMessage) (*sdkmcp.CallToolResult, any) {
	id, err := idFrom(raw, "file_id")
	if err != nil {
		return failure(err, "input", "bad_request")
	}
	res, err := s.client.ListLinks(ctx, id)
	if err != nil {
		return failure(err, "service", "request_failed")
	}
	for i := range res.Value.Links {
		res.Value.Links[i].URL = ""
	}
	return success(res.Value, fmt.Sprintf("Listed %d link(s); bearer URLs are not recoverable.", len(res.Value.Links)))
}

func (s *Service) revokeLink(ctx context.Context, _ *sdkmcp.CallToolRequest, raw json.RawMessage) (*sdkmcp.CallToolResult, any) {
	id, err := idFrom(raw, "link_id")
	if err != nil {
		return failure(err, "input", "bad_request")
	}
	if err := s.client.RevokeLink(ctx, id); err != nil {
		return failure(err, "service", "revoke_failed")
	}
	out := struct {
		OK bool   `json:"ok"`
		ID string `json:"id"`
	}{true, id}
	return success(out, "Revoked public link "+id+"; the underlying file remains.")
}

func (s *Service) deleteFile(ctx context.Context, _ *sdkmcp.CallToolRequest, raw json.RawMessage) (*sdkmcp.CallToolResult, any) {
	id, err := idFrom(raw, "file_id")
	if err != nil {
		return failure(err, "input", "bad_request")
	}
	if err := s.client.DeleteFile(ctx, id); err != nil {
		return failureNonRetryable(err, "service", "delete_failed")
	}
	out := struct {
		OK bool   `json:"ok"`
		ID string `json:"id"`
	}{true, id}
	return success(out, "Permanently deleted file "+id+" and revoked its links.")
}

func randomID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("crypto/rand unavailable")
	}
	return hex.EncodeToString(b[:])
}

func emptySchema() map[string]any { return object(nil, nil) }
func idSchema(name string) map[string]any {
	return object(map[string]any{name: map[string]any{"type": "string", "minLength": 1}}, []string{name})
}
func listSchema() map[string]any {
	return object(map[string]any{"limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 100, "default": 50}, "cursor": map[string]any{"type": "string", "minLength": 1}}, nil)
}
func uploadSchema() map[string]any {
	return object(map[string]any{
		"path":          map[string]any{"type": "string", "minLength": 1, "description": "Local path within an allowed root."},
		"name":          map[string]any{"type": "string", "minLength": 1, "maxLength": 200, "description": "Optional stored basename; defaults to the local basename."},
		"content_type":  map[string]any{"type": "string", "minLength": 1, "maxLength": 255},
		"expires_in":    map[string]any{"type": "integer", "minimum": 1, "description": "Lifetime in seconds; omit for the service default."},
		"visibility":    map[string]any{"type": "string", "enum": []string{"private", "account"}, "default": "private", "description": "account makes the file readable by sibling keys; it is not public."},
		"verify_sha256": map[string]any{"type": "boolean", "default": true},
	}, []string{"path"})
}
func downloadSchema() map[string]any {
	return object(map[string]any{
		"file_id": map[string]any{"type": "string", "minLength": 1}, "output_path": map[string]any{"type": "string", "minLength": 1, "description": "Destination within an allowed root."}, "overwrite": map[string]any{"type": "boolean", "default": false},
	}, []string{"file_id", "output_path"})
}
func createLinkSchema() map[string]any {
	return object(map[string]any{
		"file_id": map[string]any{"type": "string", "minLength": 1}, "expires_in": map[string]any{"type": "integer", "minimum": 1, "maximum": 86400, "default": 3600, "description": "Public-link lifetime in seconds, capped at 24 hours."}, "max_downloads": map[string]any{"type": "integer", "minimum": 1, "maximum": 1000, "description": "Omit for unlimited downloads; prefer 1 for sensitive material."},
	}, []string{"file_id"})
}
func object(properties map[string]any, required []string) map[string]any {
	if properties == nil {
		properties = map[string]any{}
	}
	m := map[string]any{"type": "object", "additionalProperties": false, "properties": properties}
	if required == nil {
		required = []string{}
	}
	m["required"] = required
	return m
}
