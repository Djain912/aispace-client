package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/url"
)

type PairingCode struct {
	ID               string `json:"id"`
	TransferID       string `json:"transfer_id"`
	Code             string `json:"code"`
	DeviceID         string `json:"device_id"`
	DeviceCapability string `json:"device_capability"`
	State            string `json:"state"`
	PollInterval     int    `json:"poll_interval"`
	ExpiresAt        int64  `json:"expires_at"`
}

type PairingIntentSummary struct {
	Origin                 string  `json:"origin"`
	Mode                   string  `json:"mode"`
	TransferID             string  `json:"transfer_id"`
	ExpiresAt              int64   `json:"expires_at"`
	DeclaredPlaintextBytes int64   `json:"declared_plaintext_bytes"`
	FileCount              int     `json:"file_count"`
	Sender                 *string `json:"sender"`
}

type PairingAttempt struct {
	AttemptID         string               `json:"attempt_id"`
	AttemptCapability string               `json:"attempt_capability"`
	Intent            PairingIntentSummary `json:"intent"`
	State             string               `json:"state"`
	PollInterval      int                  `json:"poll_interval"`
	ExpiresAt         int64                `json:"expires_at"`
}

type PairingDeviceStatus struct {
	State             string  `json:"state"`
	AttemptID         *string `json:"attempt_id"`
	ReceiverPublicKey *string `json:"receiver_public_key"`
	ExpiresAt         int64   `json:"expires_at"`
}

type PairingAttemptStatus struct {
	State     string  `json:"state"`
	Envelope  *string `json:"envelope"`
	ExpiresAt int64   `json:"expires_at"`
}

type PairingApproval struct {
	State string `json:"state"`
}

type PairingBinding struct {
	State string `json:"state"`
}

func (c *Client) CreatePairingCode(ctx context.Context, transferID, idempotencyKey string) (Result[PairingCode], error) {
	req, err := c.newRequest(ctx, http.MethodPost, "/v1/transfers/"+url.PathEscape(transferID)+"/pairing-codes", bytes.NewReader([]byte("{}")))
	if err != nil {
		return Result[PairingCode]{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	setIdempotencyKey(req, idempotencyKey)
	return doIdempotent[PairingCode](c, req, http.StatusCreated)
}

func (c *Client) RevokePairingCode(ctx context.Context, transferID, pairingID string) error {
	req, err := c.newRequest(ctx, http.MethodDelete, "/v1/transfers/"+url.PathEscape(transferID)+"/pairing-codes/"+url.PathEscape(pairingID), nil)
	if err != nil {
		return err
	}
	_, err = do[struct{}](c, req, http.StatusNoContent)
	return err
}

func (c *Client) AttemptPairing(ctx context.Context, code, receiverPublicKey, attemptNonce string) (Result[PairingAttempt], error) {
	body, _ := json.Marshal(map[string]string{"receiver_public_key": receiverPublicKey, "attempt_nonce": attemptNonce})
	req, err := c.unauthenticatedRequest(ctx, http.MethodPost, "/pair/"+url.PathEscape(code)+"/attempts", bytes.NewReader(body))
	if err != nil {
		return Result[PairingAttempt]{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	return do[PairingAttempt](c, req, http.StatusCreated)
}

func (c *Client) ApprovePairing(ctx context.Context, attemptID, capability string) (Result[PairingApproval], error) {
	req, err := c.publicRequest(ctx, http.MethodPost, "/pair/attempts/"+url.PathEscape(attemptID)+"/approve", capability, bytes.NewReader(nil))
	if err != nil {
		return Result[PairingApproval]{}, err
	}
	return do[PairingApproval](c, req, http.StatusOK)
}

func (c *Client) PollPairingDevice(ctx context.Context, deviceID, capability string) (Result[PairingDeviceStatus], error) {
	req, err := c.publicRequest(ctx, http.MethodGet, "/pair/devices/"+url.PathEscape(deviceID), capability, nil)
	if err != nil {
		return Result[PairingDeviceStatus]{}, err
	}
	return do[PairingDeviceStatus](c, req, http.StatusOK)
}

func (c *Client) BindPairing(ctx context.Context, deviceID, capability, attemptID, envelope string) (Result[PairingBinding], error) {
	body, _ := json.Marshal(map[string]string{"attempt_id": attemptID, "envelope": envelope})
	req, err := c.publicRequest(ctx, http.MethodPost, "/pair/devices/"+url.PathEscape(deviceID)+"/bind", capability, bytes.NewReader(body))
	if err != nil {
		return Result[PairingBinding]{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	return do[PairingBinding](c, req, http.StatusOK)
}

func (c *Client) PollPairingAttempt(ctx context.Context, attemptID, capability string) (Result[PairingAttemptStatus], error) {
	req, err := c.publicRequest(ctx, http.MethodGet, "/pair/attempts/"+url.PathEscape(attemptID), capability, nil)
	if err != nil {
		return Result[PairingAttemptStatus]{}, err
	}
	return do[PairingAttemptStatus](c, req, http.StatusOK)
}

func (c *Client) unauthenticatedRequest(ctx context.Context, method, path string, body *bytes.Reader) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, body)
	if err != nil {
		return nil, &Error{Code: "bad_request", Message: err.Error(), cause: err}
	}
	req.Header.Set("Accept", "application/json")
	if c.UserAgent != "" {
		req.Header.Set("User-Agent", c.UserAgent)
	}
	return req, nil
}
