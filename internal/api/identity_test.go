package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestIdentityAndInboxWireContract(t *testing.T) {
	var requests []*http.Request
	var bodies [][]byte
	c := New("https://example.test", "ask_owner", "test")
	c.HTTP = &http.Client{Transport: sealedRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		var b []byte
		if r.Body != nil {
			b, _ = io.ReadAll(r.Body)
		}
		requests = append(requests, r.Clone(r.Context()))
		bodies = append(bodies, b)
		switch r.URL.Path {
		case "/v1/identity-challenges":
			return response(201, `{"id":"C","identity_id":"I","challenge":"abc","operation":"identity.create","issued_at":10,"expires_at":310}`), nil
		case "/v1/identities":
			return response(201, `{"identity":{"id":"I","handle":"agent","display_name":"Agent","state":"active","fingerprint":"AA"}}`), nil
		case "/v1/transfers/T/deliveries":
			return response(201, `{"delivery":{"id":"D","transfer_id":"T","recipient_identity_id":"I","recipient_key_id":"K"}}`), nil
		case "/v1/inbox/D/claims":
			return response(201, `{"claim_id":"C1","claim_nonce":"N","lease_expires_at":20,"delivery":{"id":"D","transfer_id":"T","recipient_identity_id":"I","recipient_key_id":"K"},"transfer":{"id":"T"},"sender_signing_key":null,"sender_identity_state":null}`), nil
		case "/v1/inbox/D/content":
			resp := response(206, "cipher")
			return resp, nil
		case "/v1/inbox/D/receipts":
			return response(201, `{"receipt":{"id":"R","delivery_id":"D","type":"verified","actor_kind":"identity","signer_key_id":"S","manifest_sha256":"abc","event_at":10,"received_at":11,"signature":"sig"}}`), nil
		default:
			t.Fatalf("unexpected %s %s", r.Method, r.URL.Path)
			return nil, nil
		}
	})}
	challenge, err := c.CreateIdentityChallenge(context.Background(), "identity.create", "", "challenge-idem")
	if err != nil || challenge.Value.IdentityID != "I" {
		t.Fatal(err)
	}
	key := InitialIdentityKey{ID: "K", Algorithm: "X25519", PublicKey: "pk", CreatedAt: 10}
	if _, err := c.CreateIdentity(context.Background(), CreateIdentityRequest{IdentityID: "I", ChallengeID: "C", Challenge: "abc", EncryptionKey: key, SigningKey: InitialIdentityKey{ID: "S", Algorithm: "Ed25519", PublicKey: "spk", CreatedAt: 10}}, "idem"); err != nil {
		t.Fatal(err)
	}
	sender, keyID := "SI", "SK"
	delivery, err := c.PrepareDelivery(context.Background(), "T", PrepareDeliveryRequest{RecipientIdentityID: "I", RecipientKeyID: "K", WrappedMasterKey: "wrap", SenderIdentityID: &sender, SenderSigningKeyID: &keyID, ManifestSHA256: "abc", AlsoLink: true}, "delivery-idem")
	if err != nil || delivery.Value.Delivery.ID != "D" {
		t.Fatal(err)
	}
	claim, err := c.ClaimInboxDelivery(context.Background(), "D", "I", "claim-idem")
	if err != nil {
		t.Fatal(err)
	}
	stream, err := c.InboxContent(context.Background(), "D", claim.Value.ClaimID, claim.Value.ClaimNonce, "bytes=5-")
	if err != nil {
		t.Fatal(err)
	}
	_ = stream.Body.Close()
	event := DeliveryReceiptEvent{Receipt: SignedDeliveryReceipt{Protocol: "aispace-delivery-receipt-v1", ReceiptID: "R", DeliveryID: "D", TransferID: "T", Type: "verified", RecipientIdentityID: "I", SigningKeyID: "S", ClaimID: "C1", ClaimNonce: "N", ManifestSHA256: "abc", EventAt: 10}, Signature: "sig"}
	receipt, err := c.SubmitInboxReceipt(context.Background(), "D", event, false, "receipt-idem")
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Value.Receipt.ID != "R" {
		t.Fatalf("receipt view ID %q", receipt.Value.Receipt.ID)
	}
	if requests[4].Header.Get("X-Claim-ID") != "C1" || requests[4].Header.Get("X-Claim-Nonce") != "N" || requests[4].Header.Get("Range") != "bytes=5-" {
		t.Fatalf("claim headers %+v", requests[4].Header)
	}
	if requests[5].Header.Get("X-Claim-ID") != "C1" || requests[5].Header.Get("X-Claim-Nonce") != "N" {
		t.Fatalf("receipt claim headers %+v", requests[5].Header)
	}
	for _, i := range []int{0, 1, 2, 3, 5} {
		if requests[i].Header.Get("Idempotency-Key") == "" {
			t.Fatalf("request %d lacks idempotency key", i)
		}
	}
	var create map[string]any
	if err := json.Unmarshal(bodies[1], &create); err != nil {
		t.Fatal(err)
	}
	if create["challenge"] != "abc" {
		t.Fatalf("create body %s", bodies[1])
	}
	if strings.Contains(string(bodies[1]), "private") {
		t.Fatalf("private material in wire body: %s", bodies[1])
	}
	var prepare map[string]any
	if err := json.Unmarshal(bodies[2], &prepare); err != nil || prepare["also_link"] != true {
		t.Fatalf("prepare body %s: %v", bodies[2], err)
	}
}

func TestDeleteRecipientPinIsRepeatSafeWithNoContent(t *testing.T) {
	const identityID = "01HZZZZZZZZZZZZZZZZZZZZZZZ"
	calls := 0
	c := New("https://example.test", "ask_owner", "test")
	c.HTTP = &http.Client{Transport: sealedRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.Method != http.MethodDelete || r.URL.Path != "/v1/recipient-pins/"+identityID {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer ask_owner" {
			t.Fatalf("authorization header %q", r.Header.Get("Authorization"))
		}
		return response(http.StatusNoContent, ""), nil
	})}
	for i := 0; i < 2; i++ {
		if err := c.DeleteRecipientPin(context.Background(), identityID); err != nil {
			t.Fatalf("delete %d: %v", i+1, err)
		}
	}
	if calls != 2 {
		t.Fatalf("got %d DELETE requests", calls)
	}
}
