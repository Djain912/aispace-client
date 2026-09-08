package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// TransferCreateRequest is the service-visible sealed transfer declaration.
// MaxDownloads nil means unlimited sequential verified commits.
type TransferCreateRequest struct {
	Protocol               string `json:"protocol"`
	DeclaredPlaintextBytes int64  `json:"declared_plaintext_bytes"`
	CiphertextBytes        int64  `json:"ciphertext_bytes"`
	FileCount              int    `json:"file_count"`
	PartCount              int    `json:"part_count"`
	PartSize               int64  `json:"part_size"`
	ExpiresIn              int64  `json:"expires_in,omitempty"`
	MaxDownloads           *int64 `json:"max_downloads"`
	ClaimCapability        string `json:"claim_capability"`
	TransportMode          string `json:"transport_mode,omitempty"`
	DurabilityPolicy       string `json:"durability_policy,omitempty"`
}

type transferCompleteRequest struct {
	ManifestSHA256   string `json:"manifest_sha256"`
	CiphertextSHA256 string `json:"ciphertext_sha256"`
}

type claimProgressRequest struct {
	DownloadedBytes int64 `json:"downloaded_bytes"`
}

type transferCommitRequest struct {
	ManifestSHA256   string `json:"manifest_sha256"`
	CiphertextSHA256 string `json:"ciphertext_sha256"`
}

// CreateTransfer reserves a transfer and returns its upload/revoke capabilities.
func (c *Client) CreateTransfer(ctx context.Context, in TransferCreateRequest, idempotencyKey string) (Result[Transfer], error) {
	body, err := json.Marshal(in)
	if err != nil {
		return Result[Transfer]{}, err
	}
	req, err := c.newRequest(ctx, http.MethodPost, "/v1/transfers", bytes.NewReader(body))
	if err != nil {
		return Result[Transfer]{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	setIdempotencyKey(req, idempotencyKey)
	return doIdempotent[Transfer](c, req, http.StatusCreated)
}

// GetTransfer returns owner-visible status and already uploaded parts.
func (c *Client) GetTransfer(ctx context.Context, id string) (Result[Transfer], error) {
	req, err := c.newRequest(ctx, http.MethodGet, "/v1/transfers/"+url.PathEscape(id), nil)
	if err != nil {
		return Result[Transfer]{}, err
	}
	return doIdempotent[Transfer](c, req, http.StatusOK)
}

// UploadTransferPart uploads one exact ciphertext part.
func (c *Client) UploadTransferPart(ctx context.Context, id string, number int, body io.Reader, size int64, sha256, uploadCapability, idempotencyKey string) (Result[TransferPart], error) {
	transferCtx, watch := startTransferWatch(ctx, c.InactivityTimeout)
	defer watch.stop()
	if watch != nil {
		body = &progressReader{r: body, watch: watch}
	}
	path := "/v1/transfers/" + url.PathEscape(id) + "/parts/" + strconv.Itoa(number)
	req, err := c.newRequest(transferCtx, http.MethodPut, path, body)
	if err != nil {
		return Result[TransferPart]{}, err
	}
	req.ContentLength = size
	if size == 0 {
		req.Body = http.NoBody
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("X-Upload-Capability", uploadCapability)
	if sha256 != "" {
		req.Header.Set("X-SHA256", sha256)
	}
	setIdempotencyKey(req, idempotencyKey)
	res, err := doRequest[TransferPart](c, req, http.StatusOK, watch)
	if errors.Is(context.Cause(transferCtx), ErrTransferStalled) {
		return Result[TransferPart]{}, stalledError()
	}
	return res, err
}

// UploadTransferManifest stores the nonce-prefixed encrypted manifest.
func (c *Client) UploadTransferManifest(ctx context.Context, id string, body io.Reader, size int64, sha256, uploadCapability, idempotencyKey string) error {
	req, err := c.newRequest(ctx, http.MethodPut, "/v1/transfers/"+url.PathEscape(id)+"/manifest", body)
	if err != nil {
		return err
	}
	req.ContentLength = size
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("X-Upload-Capability", uploadCapability)
	req.Header.Set("X-SHA256", sha256)
	setIdempotencyKey(req, idempotencyKey)
	res, err := do[struct {
		ManifestSHA256 string `json:"manifest_sha256"`
	}](c, req, http.StatusOK)
	if err != nil {
		return err
	}
	if !strings.EqualFold(res.Value.ManifestSHA256, sha256) {
		return &Error{Status: http.StatusOK, Code: "bad_response", Message: "server returned a different manifest digest"}
	}
	return nil
}

// CompleteTransfer atomically finalizes the uploaded parts and manifest.
func (c *Client) CompleteTransfer(ctx context.Context, id, manifestSHA256, ciphertextSHA256, uploadCapability, idempotencyKey string) (Result[Transfer], error) {
	body, err := json.Marshal(transferCompleteRequest{ManifestSHA256: manifestSHA256, CiphertextSHA256: ciphertextSHA256})
	if err != nil {
		return Result[Transfer]{}, err
	}
	req, err := c.newRequest(ctx, http.MethodPost, "/v1/transfers/"+url.PathEscape(id)+"/complete", bytes.NewReader(body))
	if err != nil {
		return Result[Transfer]{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Upload-Capability", uploadCapability)
	setIdempotencyKey(req, idempotencyKey)
	return doIdempotent[Transfer](c, req, http.StatusOK)
}

// RevokeTransfer aborts an upload or revokes an available transfer.
func (c *Client) RevokeTransfer(ctx context.Context, id, revokeCapability, idempotencyKey string) error {
	req, err := c.newRequest(ctx, http.MethodDelete, "/v1/transfers/"+url.PathEscape(id), nil)
	if err != nil {
		return err
	}
	req.Header.Set("X-Revoke-Capability", revokeCapability)
	setIdempotencyKey(req, idempotencyKey)
	_, err = do[struct{}](c, req, http.StatusNoContent)
	return err
}

// GetTransferManifest fetches encrypted private metadata without claiming.
func (c *Client) GetTransferManifest(ctx context.Context, id, claimCapability string) (*http.Response, error) {
	return c.publicStream(ctx, http.MethodGet, "/t/"+url.PathEscape(id)+"/manifest", claimCapability, "")
}

// ClaimTransfer creates a short-lived exclusive claim lease.
func (c *Client) ClaimTransfer(ctx context.Context, id, claimCapability, idempotencyKey string) (Result[TransferClaim], error) {
	req, err := c.publicRequest(ctx, http.MethodPost, "/t/"+url.PathEscape(id)+"/claims", claimCapability, bytes.NewReader(nil))
	if err != nil {
		return Result[TransferClaim]{}, err
	}
	setIdempotencyKey(req, idempotencyKey)
	return doIdempotent[TransferClaim](c, req, http.StatusCreated)
}

// DownloadTransferContent opens a claim-authorized ciphertext stream. A
// non-empty byteRange must be an HTTP Range value such as "bytes=42-".
func (c *Client) DownloadTransferContent(ctx context.Context, id, claimToken, byteRange string) (*http.Response, error) {
	return c.publicStream(ctx, http.MethodGet, "/t/"+url.PathEscape(id)+"/content", claimToken, byteRange)
}

// RenewTransferClaim records progress and extends an active claim lease.
func (c *Client) RenewTransferClaim(ctx context.Context, transferID, claimID, claimToken string, receivedBytes int64) (Result[TransferClaim], error) {
	body, _ := json.Marshal(claimProgressRequest{DownloadedBytes: receivedBytes})
	req, err := c.publicRequest(ctx, http.MethodPatch, claimPath(transferID, claimID), claimToken, bytes.NewReader(body))
	if err != nil {
		return Result[TransferClaim]{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	return do[TransferClaim](c, req, http.StatusOK)
}

// ReleaseTransferClaim releases a claim without consuming a download.
func (c *Client) ReleaseTransferClaim(ctx context.Context, transferID, claimID, claimToken string) error {
	req, err := c.publicRequest(ctx, http.MethodDelete, claimPath(transferID, claimID), claimToken, nil)
	if err != nil {
		return err
	}
	_, err = do[struct{}](c, req, http.StatusNoContent)
	return err
}

// CommitTransferClaim records that all ciphertext and plaintext hashes were verified.
func (c *Client) CommitTransferClaim(ctx context.Context, transferID, claimID, claimToken, manifestSHA256, ciphertextSHA256, idempotencyKey string) (Result[TransferReceipt], error) {
	body, _ := json.Marshal(transferCommitRequest{ManifestSHA256: manifestSHA256, CiphertextSHA256: ciphertextSHA256})
	req, err := c.publicRequest(ctx, http.MethodPost, claimPath(transferID, claimID)+"/commit", claimToken, bytes.NewReader(body))
	if err != nil {
		return Result[TransferReceipt]{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	setIdempotencyKey(req, idempotencyKey)
	return doIdempotent[TransferReceipt](c, req, http.StatusOK)
}

// doIdempotent retries one ambiguous network failure with the same body and
// Idempotency-Key. The server can then replay a response that was committed
// before the connection was lost.
func doIdempotent[T any](c *Client, req *http.Request, wantStatus int) (Result[T], error) {
	result, err := do[T](c, req, wantStatus)
	if err == nil || req.Context().Err() != nil || req.GetBody == nil {
		return result, err
	}
	var apiErr *Error
	if !errors.As(err, &apiErr) || apiErr.Code != "network" {
		return result, err
	}
	body, bodyErr := req.GetBody()
	if bodyErr != nil {
		return result, err
	}
	retry := req.Clone(req.Context())
	retry.Body = body
	return do[T](c, retry, wantStatus)
}

func claimPath(transferID, claimID string) string {
	return "/t/" + url.PathEscape(transferID) + "/claims/" + url.PathEscape(claimID)
}

func setIdempotencyKey(req *http.Request, key string) {
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
}

func (c *Client) publicRequest(ctx context.Context, method, path, bearer string, body io.Reader) (*http.Request, error) {
	if c.BaseURL == "" {
		return nil, &Error{Code: "config", Message: "no base URL configured"}
	}
	if bearer == "" {
		return nil, &Error{Code: "bad_request", Message: "missing transfer capability"}
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, body)
	if err != nil {
		return nil, &Error{Code: "bad_request", Message: err.Error(), cause: err}
	}
	req.Header.Set("Authorization", "Bearer "+bearer)
	req.Header.Set("Accept", "application/json")
	if c.UserAgent != "" {
		req.Header.Set("User-Agent", c.UserAgent)
	}
	return req, nil
}

func (c *Client) publicStream(ctx context.Context, method, path, bearer, byteRange string) (*http.Response, error) {
	transferCtx, watch := startTransferWatch(ctx, c.InactivityTimeout)
	req, err := c.publicRequest(transferCtx, method, path, bearer, nil)
	if err != nil {
		watch.stop()
		return nil, err
	}
	if byteRange != "" {
		req.Header.Set("Range", byteRange)
	}
	httpc := c.HTTP
	if httpc == nil {
		httpc = http.DefaultClient
	}
	resp, err := httpc.Do(req)
	if err != nil {
		watch.stop()
		if errors.Is(context.Cause(transferCtx), ErrTransferStalled) {
			return nil, stalledError()
		}
		return nil, networkError(err)
	}
	want := http.StatusOK
	if byteRange != "" {
		want = http.StatusPartialContent
	}
	if resp.StatusCode != want {
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		_ = resp.Body.Close()
		watch.stop()
		if readErr != nil {
			return nil, networkError(fmt.Errorf("reading response: %w", readErr))
		}
		return nil, errorFromResponse(resp, body)
	}
	if watch != nil {
		watch.touch()
		resp.Body = &watchedBody{ReadCloser: resp.Body, ctx: transferCtx, watch: watch}
	}
	return resp, nil
}
