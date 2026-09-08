package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/url"

	"github.com/aispace-sh/aispace-client/internal/adaptive"
)

type LiveSessionCreateRequest struct {
	SignalingCapability string   `json:"signaling_capability"`
	PrivacyMode         string   `json:"privacy_mode"`
	ClientCapabilities  []string `json:"client_capabilities"`
}

type LiveSessionCloseRequest struct {
	Outcome           string `json:"outcome"`
	SelectedTransport string `json:"selected_transport"`
	NegotiationMS     int    `json:"negotiation_ms"`
}

type LiveSession struct {
	ID                  string   `json:"id"`
	Protocol            string   `json:"protocol"`
	TransferID          string   `json:"transfer_id"`
	Status              string   `json:"status"`
	PrivacyMode         string   `json:"privacy_mode"`
	CandidateTransports []string `json:"candidate_transports,omitempty"`
	SelectedTransport   *string  `json:"selected_transport"`
	Outcome             *string  `json:"outcome"`
	CreatedAt           int64    `json:"created_at"`
	ExpiresAt           int64    `json:"expires_at"`
	ClosedAt            *int64   `json:"closed_at"`
}

func (c *Client) GetTransportCapabilities(ctx context.Context) (Result[adaptive.ServiceCapabilities], error) {
	req, err := c.newRequest(ctx, http.MethodGet, "/v1/transports", nil)
	if err != nil {
		return Result[adaptive.ServiceCapabilities]{}, err
	}
	return doIdempotent[adaptive.ServiceCapabilities](c, req, http.StatusOK)
}

func (c *Client) CreateLiveSession(ctx context.Context, transferID string, in LiveSessionCreateRequest, idempotencyKey string) (Result[LiveSession], error) {
	body, err := json.Marshal(in)
	if err != nil {
		return Result[LiveSession]{}, err
	}
	req, err := c.newRequest(ctx, http.MethodPost, "/v1/transfers/"+url.PathEscape(transferID)+"/live-sessions", bytes.NewReader(body))
	if err != nil {
		return Result[LiveSession]{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	setIdempotencyKey(req, idempotencyKey)
	return doIdempotent[LiveSession](c, req, http.StatusCreated)
}

func (c *Client) CloseLiveSession(ctx context.Context, transferID, sessionID, uploadCapability string, in LiveSessionCloseRequest) (Result[LiveSession], error) {
	body, err := json.Marshal(in)
	if err != nil {
		return Result[LiveSession]{}, err
	}
	path := "/v1/transfers/" + url.PathEscape(transferID) + "/live-sessions/" + url.PathEscape(sessionID) + "/close"
	req, err := c.newRequest(ctx, http.MethodPost, path, bytes.NewReader(body))
	if err != nil {
		return Result[LiveSession]{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Upload-Capability", uploadCapability)
	return doIdempotent[LiveSession](c, req, http.StatusOK)
}
