package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAdaptiveTransportAPI(t *testing.T) {
	var step int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		step++
		w.Header().Set("Content-Type", "application/json")
		switch step {
		case 1:
			if r.Method != http.MethodGet || r.URL.Path != "/v1/transports" {
				t.Errorf("discovery request = %s %s", r.Method, r.URL.Path)
			}
			_, _ = w.Write([]byte(`{"protocol":"aispace-adaptive-v1","enabled":true,"supported_modes":["stored","adaptive"],"transports":{"r2":{"available":true,"durable":true},"direct":{"available":false,"durable":false},"turn":{"available":false,"durable":false},"native_relay":{"available":false,"durable":false}}}`))
		case 2:
			if r.Method != http.MethodPost || r.URL.Path != "/v1/transfers/transfer/live-sessions" || r.Header.Get("Idempotency-Key") != "idem" {
				t.Errorf("create request = %s %s idempotency=%q", r.Method, r.URL.Path, r.Header.Get("Idempotency-Key"))
			}
			var body LiveSessionCreateRequest
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.SignalingCapability != "secret" || body.PrivacyMode != "relay_only" {
				t.Errorf("create body = %+v err=%v", body, err)
			}
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":"session","protocol":"aispace-adaptive-v1","transfer_id":"transfer","status":"negotiating"}`))
		case 3:
			if r.Method != http.MethodPost || r.URL.Path != "/v1/transfers/transfer/live-sessions/session/close" || r.Header.Get("X-Upload-Capability") != "upload" {
				t.Errorf("close request = %s %s cap=%q", r.Method, r.URL.Path, r.Header.Get("X-Upload-Capability"))
			}
			_, _ = w.Write([]byte(`{"id":"session","protocol":"aispace-adaptive-v1","transfer_id":"transfer","status":"closed","selected_transport":"r2","outcome":"timeout"}`))
		default:
			t.Errorf("unexpected request %d", step)
		}
	}))
	defer server.Close()
	c := &Client{BaseURL: server.URL, Key: "ask_test", HTTP: server.Client()}
	if result, err := c.GetTransportCapabilities(context.Background()); err != nil || !result.Value.Enabled {
		t.Fatalf("discovery: %+v err=%v", result.Value, err)
	}
	created, err := c.CreateLiveSession(context.Background(), "transfer", LiveSessionCreateRequest{SignalingCapability: "secret", PrivacyMode: "relay_only", ClientCapabilities: []string{"webrtc-datachannel-v1"}}, "idem")
	if err != nil || created.Value.ID != "session" {
		t.Fatalf("create: %+v err=%v", created.Value, err)
	}
	closed, err := c.CloseLiveSession(context.Background(), "transfer", "session", "upload", LiveSessionCloseRequest{Outcome: "timeout", SelectedTransport: "r2", NegotiationMS: 2000})
	if err != nil || closed.Value.Status != "closed" {
		t.Fatalf("close: %+v err=%v", closed.Value, err)
	}
}
