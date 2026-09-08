package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/aispace-sh/aispace-client/internal/api"
	"github.com/aispace-sh/aispace-client/internal/config"
	"github.com/aispace-sh/aispace-client/internal/handoff"
	"github.com/aispace-sh/aispace-client/internal/sealed"
)

func TestCanonicalPairCode(t *testing.T) {
	for input, want := range map[string]string{"j7km-pqrt": "J7KM-PQRT", "J7KMPQRT": "J7KM-PQRT"} {
		got, err := canonicalPairCode(input)
		if err != nil || got != want {
			t.Fatalf("canonicalPairCode(%q) = %q, %v", input, got, err)
		}
	}
	for _, input := range []string{"J7KM-PQR", "J7KM-PQR0", "J7KM-PQRL", "J7KM-PQR!"} {
		if _, err := canonicalPairCode(input); err == nil {
			t.Fatalf("accepted %q", input)
		}
	}
}

func TestHandoffSenderLanguage(t *testing.T) {
	if got := handoffSender(nil); got != "Unknown sender" {
		t.Fatalf("got %q", got)
	}
	verified := "Verified Research agent"
	if got := handoffSender(&verified); got != verified {
		t.Fatalf("got %q", got)
	}
}

func TestHandoffEncodeUsesAllCanonicalRepresentations(t *testing.T) {
	isolate(t)
	fragment := "as1." + strings.Repeat("A", 43) + "." + strings.Repeat("A", 43)
	result := run("", "handoff", "encode", "https://aispace.sh/t/01TEST#"+fragment)
	if result.code != ExitOK || !strings.Contains(result.stdout, "https   https://aispace.sh/t/01TEST#as1.") || !strings.Contains(result.stdout, "token   aispace-transfer-v1.") || !strings.Contains(result.stdout, "native  aispace://transfer/v1/") || !strings.Contains(result.stderr, "process arguments") {
		t.Fatalf("encode = %+v", result)
	}
	jsonResult := run("", "--json", "handoff", "encode", "https://aispace.sh/t/01TEST#"+fragment)
	if jsonResult.code != ExitUsage {
		t.Fatalf("json encode = %+v", jsonResult)
	}
}

func TestHandoffEncodeReadsProtectedTokenFile(t *testing.T) {
	isolate(t)
	fragment := "as1." + strings.Repeat("A", 43) + "." + strings.Repeat("A", 43)
	path := filepath.Join(t.TempDir(), "handoff-token")
	if err := os.WriteFile(path, []byte("https://aispace.sh/t/01TEST#"+fragment+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	result := run("", "handoff", "encode", "--token-file", path)
	if result.code != ExitOK || strings.Contains(result.stderr, "process arguments") {
		t.Fatalf("encode file = %+v", result)
	}
}

func TestHostileOwnerTicketOriginIsRejectedBeforeAuthenticatedRequest(t *testing.T) {
	isolate(t)
	var hostileRequests int
	hostile := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hostileRequests++ }))
	defer hostile.Close()
	ticketPath := filepath.Join(t.TempDir(), "ticket.json")
	ticketBytes, _ := json.Marshal(transferTicket{Version: 1, TransferID: "01TRANSFER", ServerURL: hostile.URL, RevokeCapability: "revoke", RecipientToken: "hostile-token"})
	if err := os.WriteFile(ticketPath, ticketBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	result := run("", "--url", "https://trusted.example", "--key", "ask_validkey0000000000000000000000", "handoff", "offer", "01TRANSFER", "--ticket", ticketPath)
	if result.code != ExitUsage || !strings.Contains(result.stderr, "ticket server does not match") {
		t.Fatalf("offer = %+v", result)
	}
	if hostileRequests != 0 {
		t.Fatalf("hostile origin received %d authenticated request(s)", hostileRequests)
	}
}

func TestHandoffOfferRetriesSameDevicePollUntilApproved(t *testing.T) {
	isolate(t)
	const transferID = "01TRANSFER"
	var polls, creates int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/transfers/"+transferID+"/pairing-codes":
			creates++
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":"01PAIRING","transfer_id":"01TRANSFER","code":"J7KM-PQRT","device_id":"01DEVICE","device_capability":"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA","state":"pending","poll_interval":1,"expires_at":` + jsonNumber(time.Now().Add(time.Minute).Unix()) + `}`))
		case r.Method == http.MethodGet && r.URL.Path == "/pair/devices/01DEVICE":
			polls++
			if polls <= 2 {
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte(`{"error":{"code":"internal","message":"retry"}}`))
				return
			}
			_, _ = w.Write([]byte(`{"state":"bound","attempt_id":null,"receiver_public_key":null,"expires_at":` + jsonNumber(time.Now().Add(time.Minute).Unix()) + `}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	writeConfig(t, config.File{Key: "ask_validkey0000000000000000000000", URL: server.URL})
	var secrets sealed.Secrets
	for index := range secrets.MasterKey {
		secrets.MasterKey[index] = 0x11
		secrets.ClaimCapability[index] = 0x22
	}
	intent, err := handoff.FromSealed(server.URL, transferID, secrets)
	if err != nil {
		t.Fatal(err)
	}
	token, _ := intent.Token()
	ticketPath := filepath.Join(t.TempDir(), "ticket.json")
	ticketBytes, _ := json.Marshal(transferTicket{Version: 1, TransferID: transferID, ServerURL: server.URL, RevokeCapability: "revoke", RecipientToken: token})
	if err := os.WriteFile(ticketPath, ticketBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	result := run("", "handoff", "offer", transferID, "--ticket", ticketPath)
	if result.code != ExitOK || creates != 1 || polls != 3 {
		t.Fatalf("offer = %+v creates=%d polls=%d", result, creates, polls)
	}
}

func TestHandoffReceiveRetainsAttemptAcrossTransientPollsAndCanonicalOrigin(t *testing.T) {
	isolate(t)
	var attempts, polls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		origin := strings.Replace(serverURLFromRequest(r), "localhost", "LOCALHOST", 1)
		expires := jsonNumber(time.Now().Add(time.Minute).Unix())
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/pair/J7KM-PQRT/attempts":
			attempts++
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"attempt_id":"01ATTEMPT","attempt_capability":"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA","intent":{"origin":` + strconv.Quote(origin) + `,"mode":"sealed-link","transfer_id":"01TRANSFER","expires_at":` + expires + `,"declared_plaintext_bytes":1,"file_count":1,"sender":null},"state":"awaiting_approval","poll_interval":1,"expires_at":` + expires + `}`))
		case r.Method == http.MethodPost && r.URL.Path == "/pair/attempts/01ATTEMPT/approve":
			_, _ = w.Write([]byte(`{"state":"approved"}`))
		case r.Method == http.MethodGet && r.URL.Path == "/pair/attempts/01ATTEMPT":
			polls++
			if polls <= 2 {
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte(`{"error":{"code":"internal","message":"retry"}}`))
				return
			}
			_, _ = w.Write([]byte(`{"state":"crowded","envelope":null,"expires_at":` + expires + `}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	configured := strings.Replace(server.URL, "127.0.0.1", "localhost", 1)
	writeConfig(t, config.File{URL: configured})
	result := run("", "handoff", "receive", "j7kmpqrt", "--yes")
	if result.code != ExitGeneric || !strings.Contains(result.stderr, "another receiver") || attempts != 1 || polls != 3 {
		t.Fatalf("receive = %+v attempts=%d polls=%d", result, attempts, polls)
	}
}

func TestHandoffReceiveRetriesAmbiguousPostsWithSameReceiverMaterial(t *testing.T) {
	isolate(t)
	var attempts, approvals int
	var attemptBodies []string
	var approvalCapabilities []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		expires := jsonNumber(time.Now().Add(time.Minute).Unix())
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/pair/J7KM-PQRT/attempts":
			attempts++
			body, _ := io.ReadAll(r.Body)
			attemptBodies = append(attemptBodies, string(body))
			if attempts == 1 {
				w.WriteHeader(http.StatusServiceUnavailable)
				_, _ = w.Write([]byte(`{"error":{"code":"internal","message":"response lost after commit"}}`))
				return
			}
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"attempt_id":"01ATTEMPT","attempt_capability":"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA","intent":{"origin":` + strconv.Quote(serverURLFromRequest(r)) + `,"mode":"sealed-link","transfer_id":"01TRANSFER","expires_at":` + expires + `,"declared_plaintext_bytes":1,"file_count":1,"sender":null},"state":"awaiting_approval","poll_interval":1,"expires_at":` + expires + `}`))
		case r.Method == http.MethodPost && r.URL.Path == "/pair/attempts/01ATTEMPT/approve":
			approvals++
			approvalCapabilities = append(approvalCapabilities, r.Header.Get("Authorization"))
			if approvals == 1 {
				w.WriteHeader(http.StatusServiceUnavailable)
				_, _ = w.Write([]byte(`{"error":{"code":"internal","message":"response lost after commit"}}`))
				return
			}
			_, _ = w.Write([]byte(`{"state":"approved"}`))
		case r.Method == http.MethodGet && r.URL.Path == "/pair/attempts/01ATTEMPT":
			_, _ = w.Write([]byte(`{"state":"crowded","envelope":null,"expires_at":` + expires + `}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	writeConfig(t, config.File{URL: server.URL})
	result := run("", "handoff", "receive", "J7KM-PQRT", "--yes")
	if result.code != ExitGeneric || !strings.Contains(result.stderr, "another receiver") {
		t.Fatalf("receive = %+v", result)
	}
	if attempts != 2 || len(attemptBodies) != 2 || attemptBodies[0] != attemptBodies[1] {
		t.Fatalf("attempts=%d bodies=%q", attempts, attemptBodies)
	}
	var attemptBody map[string]string
	if err := json.Unmarshal([]byte(attemptBodies[0]), &attemptBody); err != nil || attemptBody["receiver_public_key"] == "" || attemptBody["attempt_nonce"] == "" {
		t.Fatalf("attempt body = %#v, %v", attemptBody, err)
	}
	if approvals != 2 || len(approvalCapabilities) != 2 || approvalCapabilities[0] != approvalCapabilities[1] || approvalCapabilities[0] != "Bearer AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA" {
		t.Fatalf("approvals=%d capabilities=%q", approvals, approvalCapabilities)
	}
}

func TestPairingStateTablesFailClosed(t *testing.T) {
	attemptID := "01ATTEMPT"
	publicKey := "public-key"
	envelope := "envelope"
	for _, test := range []struct {
		name   string
		status api.PairingDeviceStatus
		action string
		code   string
	}{
		{"pending", api.PairingDeviceStatus{State: "pending"}, "wait", ""},
		{"approved", api.PairingDeviceStatus{State: "approved", AttemptID: &attemptID, ReceiverPublicKey: &publicKey}, "bind", ""},
		{"bound", api.PairingDeviceStatus{State: "bound"}, "done", ""},
		{"expired", api.PairingDeviceStatus{State: "expired"}, "", "pairing_expired"},
		{"revoked", api.PairingDeviceStatus{State: "revoked"}, "", "pairing_revoked"},
		{"unknown", api.PairingDeviceStatus{State: "future"}, "", "bad_response"},
		{"incomplete approved", api.PairingDeviceStatus{State: "approved"}, "", "bad_response"},
	} {
		t.Run("device "+test.name, func(t *testing.T) {
			action, err := devicePairingAction(test.status)
			assertPairingAction(t, action, err, test.action, test.code)
		})
	}
	for _, test := range []struct {
		name   string
		status api.PairingAttemptStatus
		action string
		code   string
	}{
		{"approved", api.PairingAttemptStatus{State: "approved"}, "wait", ""},
		{"bound", api.PairingAttemptStatus{State: "bound", Envelope: &envelope}, "open", ""},
		{"expired", api.PairingAttemptStatus{State: "expired"}, "", "pairing_expired"},
		{"revoked", api.PairingAttemptStatus{State: "revoked"}, "", "pairing_revoked"},
		{"unknown", api.PairingAttemptStatus{State: "future"}, "", "bad_response"},
		{"incomplete bound", api.PairingAttemptStatus{State: "bound"}, "", "bad_response"},
	} {
		t.Run("attempt "+test.name, func(t *testing.T) {
			action, err := attemptPairingAction(test.status)
			assertPairingAction(t, action, err, test.action, test.code)
		})
	}
	for _, test := range []struct {
		name    string
		state   string
		allowed []string
		ok      bool
	}{
		{"pairing created", "pending", []string{"pending"}, true},
		{"attempt created", "awaiting_approval", []string{"awaiting_approval"}, true},
		{"approval replay", "ready", []string{"approved", "ready"}, true},
		{"terminal creation", "expired", []string{"pending"}, false},
		{"revoked creation", "revoked", []string{"pending"}, false},
		{"unknown creation", "future", []string{"pending"}, false},
		{"unknown approval", "future", []string{"approved", "ready"}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := requirePairingState(test.state, test.allowed...)
			if test.ok && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !test.ok && test.state == "future" {
				var coded *codedError
				if !errors.As(err, &coded) || coded.code != "bad_response" {
					t.Fatalf("error = %#v, want bad_response", err)
				}
			} else if !test.ok && err == nil {
				t.Fatal("expected terminal state error")
			}
		})
	}
}

func assertPairingAction(t *testing.T, action string, err error, wantAction, wantCode string) {
	t.Helper()
	if action != wantAction {
		t.Fatalf("action = %q, want %q", action, wantAction)
	}
	if wantCode == "" {
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		return
	}
	var coded *codedError
	if !errors.As(err, &coded) || coded.code != wantCode {
		t.Fatalf("error = %#v, want code %q", err, wantCode)
	}
}

func serverURLFromRequest(request *http.Request) string {
	return "http://" + request.Host
}

func jsonNumber(value int64) string { return fmt.Sprintf("%d", value) }
