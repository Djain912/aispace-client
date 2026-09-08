package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"
)

type CreateIdentityRequest struct {
	IdentityID                 string             `json:"identity_id"`
	Handle                     string             `json:"handle"`
	DisplayName                string             `json:"display_name"`
	ChallengeID                string             `json:"challenge_id"`
	Challenge                  string             `json:"challenge"`
	EncryptionKey              InitialIdentityKey `json:"encryption_key"`
	SigningKey                 InitialIdentityKey `json:"signing_key"`
	EncryptionBindingSignature string             `json:"encryption_binding_signature"`
	PossessionSignature        string             `json:"possession_signature"`
}

type RotateIdentityKeyRequest struct {
	ChallengeID       string               `json:"challenge_id"`
	Challenge         string               `json:"challenge"`
	Purpose           string               `json:"purpose"`
	Key               SuccessorIdentityKey `json:"key"`
	PredecessorKeyID  string               `json:"predecessor_key_id"`
	RotationSignature string               `json:"rotation_signature"`
}

type PrepareDeliveryRequest struct {
	RecipientIdentityID string  `json:"recipient_identity_id"`
	RecipientKeyID      string  `json:"recipient_key_id"`
	WrappedMasterKey    string  `json:"wrapped_master_key"`
	SenderIdentityID    *string `json:"sender_identity_id"`
	SenderSigningKeyID  *string `json:"sender_signing_key_id"`
	ManifestSHA256      string  `json:"manifest_sha256"`
	AlsoLink            bool    `json:"also_link"`
}

func (c *Client) CreateIdentityChallenge(ctx context.Context, operation, identityID, idempotencyKey string) (Result[IdentityChallenge], error) {
	in := map[string]any{"operation": operation}
	if identityID != "" {
		in["identity_id"] = identityID
	}
	return postJSON[IdentityChallenge](c, ctx, "/v1/identity-challenges", in, http.StatusCreated, idempotencyKey)
}

func (c *Client) CreateIdentity(ctx context.Context, in CreateIdentityRequest, idempotencyKey string) (Result[IdentityEnvelope], error) {
	return postJSON[IdentityEnvelope](c, ctx, "/v1/identities", in, http.StatusCreated, idempotencyKey)
}

func (c *Client) ListIdentities(ctx context.Context) (Result[IdentityList], error) {
	req, err := c.newRequest(ctx, http.MethodGet, "/v1/identities", nil)
	if err != nil {
		return Result[IdentityList]{}, err
	}
	return doIdempotent[IdentityList](c, req, http.StatusOK)
}

func (c *Client) GetIdentity(ctx context.Context, id string) (Result[IdentityEnvelope], error) {
	req, err := c.newRequest(ctx, http.MethodGet, "/v1/identities/"+url.PathEscape(id), nil)
	if err != nil {
		return Result[IdentityEnvelope]{}, err
	}
	return doIdempotent[IdentityEnvelope](c, req, http.StatusOK)
}

func (c *Client) GetPublicIdentity(ctx context.Context, id string) (Result[Identity], error) {
	req, err := c.newRequest(ctx, http.MethodGet, "/i/"+url.PathEscape(id), nil)
	if err != nil {
		return Result[Identity]{}, err
	}
	return doIdempotent[Identity](c, req, http.StatusOK)
}

func (c *Client) GetPublicIdentityKey(ctx context.Context, identityID, keyID string) (Result[IdentityKeyEnvelope], error) {
	req, err := c.newRequest(ctx, http.MethodGet, "/i/"+url.PathEscape(identityID)+"/keys/"+url.PathEscape(keyID), nil)
	if err != nil {
		return Result[IdentityKeyEnvelope]{}, err
	}
	return doIdempotent[IdentityKeyEnvelope](c, req, http.StatusOK)
}

func (c *Client) PatchIdentity(ctx context.Context, id string, in any) (Result[IdentityEnvelope], error) {
	body, err := json.Marshal(in)
	if err != nil {
		return Result[IdentityEnvelope]{}, err
	}
	req, err := c.newRequest(ctx, http.MethodPatch, "/v1/identities/"+url.PathEscape(id), bytes.NewReader(body))
	if err != nil {
		return Result[IdentityEnvelope]{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	return do[IdentityEnvelope](c, req, http.StatusOK)
}

func (c *Client) RotateIdentityKey(ctx context.Context, id string, in RotateIdentityKeyRequest, idempotencyKey string) (Result[IdentityKeyEnvelope], error) {
	return postJSON[IdentityKeyEnvelope](c, ctx, "/v1/identities/"+url.PathEscape(id)+"/keys", in, http.StatusCreated, idempotencyKey)
}

func (c *Client) RevokeIdentityKey(ctx context.Context, id, keyID, idempotencyKey string) error {
	_, err := postJSON[struct{}](c, ctx, "/v1/identities/"+url.PathEscape(id)+"/keys/"+url.PathEscape(keyID)+"/revoke", struct{}{}, http.StatusNoContent, idempotencyKey)
	return err
}

func (c *Client) PrepareDelivery(ctx context.Context, transferID string, in PrepareDeliveryRequest, idempotencyKey string) (Result[DeliveryEnvelope], error) {
	return postJSON[DeliveryEnvelope](c, ctx, "/v1/transfers/"+url.PathEscape(transferID)+"/deliveries", in, http.StatusCreated, idempotencyKey)
}

func (c *Client) PutRecipientPin(ctx context.Context, in RecipientPin, idempotencyKey string) (Result[RecipientPinEnvelope], error) {
	return postJSON[RecipientPinEnvelope](c, ctx, "/v1/recipient-pins", in, http.StatusOK, idempotencyKey)
}
func (c *Client) ListRecipientPins(ctx context.Context) (Result[RecipientPinList], error) {
	req, err := c.newRequest(ctx, http.MethodGet, "/v1/recipient-pins", nil)
	if err != nil {
		return Result[RecipientPinList]{}, err
	}
	return doIdempotent[RecipientPinList](c, req, http.StatusOK)
}

func (c *Client) DeleteRecipientPin(ctx context.Context, identityID string) error {
	req, err := c.newRequest(ctx, http.MethodDelete, "/v1/recipient-pins/"+url.PathEscape(identityID), bytes.NewReader(nil))
	if err != nil {
		return err
	}
	_, err = doIdempotent[struct{}](c, req, http.StatusNoContent)
	return err
}

func (c *Client) ListInbox(ctx context.Context, cursor string, limit int) (Result[InboxList], error) {
	q := url.Values{}
	if cursor != "" {
		q.Set("cursor", cursor)
	}
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	path := "/v1/inbox"
	if len(q) > 0 {
		path += "?" + q.Encode()
	}
	req, err := c.newRequest(ctx, http.MethodGet, path, nil)
	if err != nil {
		return Result[InboxList]{}, err
	}
	return doIdempotent[InboxList](c, req, http.StatusOK)
}

func (c *Client) ClaimInboxDelivery(ctx context.Context, deliveryID, identityID, idempotencyKey string) (Result[InboxClaim], error) {
	return postJSON[InboxClaim](c, ctx, "/v1/inbox/"+url.PathEscape(deliveryID)+"/claims", map[string]string{"recipient_identity_id": identityID}, http.StatusCreated, idempotencyKey)
}

func (c *Client) InboxManifest(ctx context.Context, deliveryID, claimID, claimNonce string) (*http.Response, error) {
	return c.inboxStream(ctx, "/v1/inbox/"+url.PathEscape(deliveryID)+"/manifest", claimID, claimNonce, "")
}

func (c *Client) InboxContent(ctx context.Context, deliveryID, claimID, claimNonce, byteRange string) (*http.Response, error) {
	return c.inboxStream(ctx, "/v1/inbox/"+url.PathEscape(deliveryID)+"/content", claimID, claimNonce, byteRange)
}

func (c *Client) RenewInboxClaim(ctx context.Context, deliveryID, claimID, claimNonce string, downloadedBytes int64) (Result[InboxClaim], error) {
	body, _ := json.Marshal(map[string]int64{"downloaded_bytes": downloadedBytes})
	req, err := c.newRequest(ctx, http.MethodPatch, "/v1/inbox/"+url.PathEscape(deliveryID)+"/claims/"+url.PathEscape(claimID), bytes.NewReader(body))
	if err != nil {
		return Result[InboxClaim]{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Claim-ID", claimID)
	req.Header.Set("X-Claim-Nonce", claimNonce)
	return do[InboxClaim](c, req, http.StatusOK)
}

func (c *Client) SubmitInboxReceipt(ctx context.Context, deliveryID string, in DeliveryReceiptEvent, reject bool, idempotencyKey string) (Result[DeliveryReceiptEnvelope], error) {
	suffix := "/receipts"
	if reject {
		suffix = "/reject"
	}
	body, err := json.Marshal(in)
	if err != nil {
		return Result[DeliveryReceiptEnvelope]{}, err
	}
	req, err := c.newRequest(ctx, http.MethodPost, "/v1/inbox/"+url.PathEscape(deliveryID)+suffix, bytes.NewReader(body))
	if err != nil {
		return Result[DeliveryReceiptEnvelope]{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Claim-ID", in.Receipt.ClaimID)
	req.Header.Set("X-Claim-Nonce", in.Receipt.ClaimNonce)
	setIdempotencyKey(req, idempotencyKey)
	return doIdempotent[DeliveryReceiptEnvelope](c, req, http.StatusCreated)
}

func (c *Client) ListDeliveryReceipts(ctx context.Context, deliveryID string) (Result[DeliveryReceiptList], error) {
	req, err := c.newRequest(ctx, http.MethodGet, "/v1/deliveries/"+url.PathEscape(deliveryID)+"/receipts", nil)
	if err != nil {
		return Result[DeliveryReceiptList]{}, err
	}
	return doIdempotent[DeliveryReceiptList](c, req, http.StatusOK)
}

func postJSON[T any](c *Client, ctx context.Context, path string, in any, status int, idempotencyKey string) (Result[T], error) {
	body, err := json.Marshal(in)
	if err != nil {
		return Result[T]{}, err
	}
	req, err := c.newRequest(ctx, http.MethodPost, path, bytes.NewReader(body))
	if err != nil {
		return Result[T]{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	setIdempotencyKey(req, idempotencyKey)
	return doIdempotent[T](c, req, status)
}

func (c *Client) inboxStream(ctx context.Context, path, claimID, claimNonce, byteRange string) (*http.Response, error) {
	transferCtx, watch := startTransferWatch(ctx, c.InactivityTimeout)
	req, err := c.newRequest(transferCtx, http.MethodGet, path, nil)
	if err != nil {
		watch.stop()
		return nil, err
	}
	req.Header.Set("X-Claim-ID", claimID)
	req.Header.Set("X-Claim-Nonce", claimNonce)
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
		return nil, networkError(err)
	}
	want := http.StatusOK
	if byteRange != "" {
		want = http.StatusPartialContent
	}
	if resp.StatusCode != want {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		_ = resp.Body.Close()
		watch.stop()
		return nil, errorFromResponse(resp, b)
	}
	if watch != nil {
		watch.touch()
		resp.Body = &watchedBody{ReadCloser: resp.Body, ctx: transferCtx, watch: watch}
	}
	return resp, nil
}
