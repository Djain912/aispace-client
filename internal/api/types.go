package api

import "encoding/json"

// File mirrors the API File object.
type File struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	ContentType string `json:"content_type"`
	SizeBytes   int64  `json:"size_bytes"`
	SHA256      string `json:"sha256,omitempty"`
	EncAlg      string `json:"enc_alg,omitempty"`
	Visibility  string `json:"visibility"`
	CreatedAt   int64  `json:"created_at"`
	ExpiresAt   int64  `json:"expires_at"`
}

// ShareLink mirrors the API ShareLink object. URL is only set on creation.
type ShareLink struct {
	ID            string `json:"id"`
	FileID        string `json:"file_id"`
	URL           string `json:"url,omitempty"`
	ExpiresAt     int64  `json:"expires_at"`
	MaxDownloads  *int64 `json:"max_downloads"`
	DownloadCount int64  `json:"download_count"`
	CreatedAt     int64  `json:"created_at"`
	RevokedAt     *int64 `json:"revoked_at"`
}

// BotKey mirrors the API BotKey object.
type BotKey struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	Prefix        string `json:"prefix"`
	BudgetBytes   int64  `json:"budget_bytes"`
	BudgetLimited bool   `json:"budget_limited"`
	UsedBytes     int64  `json:"used_bytes"`
	CreatedAt     int64  `json:"created_at"`
	LastUsedAt    *int64 `json:"last_used_at"`
	RevokedAt     *int64 `json:"revoked_at"`
}

// Whoami is the GET /v1/whoami response.
type Whoami struct {
	Key  BotKey `json:"key"`
	User struct {
		Email string `json:"email"`
	} `json:"user"`
}

// Quota is the GET /v1/quota response.
type Quota struct {
	Key struct {
		BudgetBytes    int64 `json:"budget_bytes"`
		UsedBytes      int64 `json:"used_bytes"`
		RemainingBytes int64 `json:"remaining_bytes"`
		BudgetLimited  bool  `json:"budget_limited"`
	} `json:"key"`
	Account struct {
		AllowanceBytes int64  `json:"allowance_bytes"`
		UsedBytes      int64  `json:"used_bytes"`
		RemainingBytes int64  `json:"remaining_bytes"`
		Plan           string `json:"plan"`
		ExtraBlocks    int64  `json:"extra_blocks"`
	} `json:"account"`
	Month struct {
		UploadsUsed    int64 `json:"uploads_used"`
		UploadsLimit   int64 `json:"uploads_limit"`
		DownloadsUsed  int64 `json:"downloads_used"`
		DownloadsLimit int64 `json:"downloads_limit"`
		PeriodEnd      int64 `json:"period_end"`
	} `json:"month"`
	Limits struct {
		MaxFileBytes      int64 `json:"max_file_bytes"`
		MaxFileTTLSeconds int64 `json:"max_file_ttl_seconds"`
		MaxLinkTTLSeconds int64 `json:"max_link_ttl_seconds"`
		UploadsPerHour    int64 `json:"uploads_per_hour"`
		UploadsPerDay     int64 `json:"uploads_per_day"`
		RequestsPerMinute int64 `json:"requests_per_minute"`
	} `json:"limits"`
	Rate struct {
		UploadsHourRemaining    int64 `json:"uploads_hour_remaining"`
		UploadsDayRemaining     int64 `json:"uploads_day_remaining"`
		RequestsMinuteRemaining int64 `json:"requests_minute_remaining"`
	} `json:"rate"`
}

// FileList is the GET /v1/files response.
type FileList struct {
	Files      []json.RawMessage `json:"files"`
	NextCursor *string           `json:"next_cursor"`
}

// Result pairs a decoded value with the raw response body so callers can
// print exactly what the API returned.
type Result[T any] struct {
	Value T
	Raw   json.RawMessage
}

// LinkList is the GET /v1/files/:id/links response.
type LinkList struct {
	Links []ShareLink `json:"links"`
}

// Transfer is an owner-visible sealed asynchronous transfer. Secret
// capabilities are returned only when a transfer is created.
type Transfer struct {
	ID                     string         `json:"id"`
	Protocol               string         `json:"protocol"`
	State                  string         `json:"state"`
	UploadCapability       string         `json:"upload_capability,omitempty"`
	RevokeCapability       string         `json:"revoke_capability,omitempty"`
	DeclaredPlaintextBytes int64          `json:"declared_plaintext_bytes"`
	CiphertextBytes        int64          `json:"ciphertext_bytes"`
	FileCount              int            `json:"file_count"`
	PartCount              int            `json:"part_count"`
	PartSize               int64          `json:"part_size"`
	ManifestSizeBytes      *int64         `json:"manifest_size_bytes"`
	ManifestSHA256         *string        `json:"manifest_sha256"`
	CiphertextSHA256       *string        `json:"ciphertext_sha256"`
	MaxDownloads           *int64         `json:"max_downloads"`
	AlsoLink               bool           `json:"also_link"`
	CommittedCount         int64          `json:"committed_count"`
	CreatedAt              int64          `json:"created_at"`
	UploadExpiresAt        int64          `json:"upload_expires_at"`
	ExpiresAt              int64          `json:"expires_at"`
	CompletedAt            *int64         `json:"completed_at"`
	RevokedAt              *int64         `json:"revoked_at"`
	ConsumedAt             *int64         `json:"consumed_at"`
	TransportMode          string         `json:"transport_mode,omitempty"`
	DurabilityPolicy       string         `json:"durability_policy,omitempty"`
	SelectedTransport      string         `json:"selected_transport,omitempty"`
	UploadedParts          []TransferPart `json:"uploaded_parts,omitempty"`
}

// TransferPart is one uploaded byte range in the concatenated ciphertext.
type TransferPart struct {
	PartNumber int    `json:"part_number"`
	ETag       string `json:"etag"`
	SizeBytes  int64  `json:"size_bytes"`
	SHA256     string `json:"sha256,omitempty"`
}

// TransferClaim is a short-lived lease. Token is returned only at creation.
type TransferClaim struct {
	ID              string `json:"id"`
	Token           string `json:"token,omitempty"`
	State           string `json:"state,omitempty"`
	LeaseExpiresAt  int64  `json:"lease_expires_at"`
	DownloadedBytes int64  `json:"downloaded_bytes,omitempty"`
}

// TransferReceipt records a recipient's verified commit assertion.
type TransferReceipt struct {
	ID             string `json:"id,omitempty"`
	TransferID     string `json:"transfer_id"`
	ClaimID        string `json:"claim_id"`
	Status         string `json:"status"`
	ActorKind      string `json:"actor_kind"`
	CommittedAt    int64  `json:"committed_at"`
	CommittedCount int64  `json:"committed_count"`
	TransferState  string `json:"transfer_state"`
}

type IdentityKey struct {
	Protocol          string  `json:"protocol"`
	IdentityID        string  `json:"identity_id"`
	KeyID             string  `json:"key_id"`
	Purpose           string  `json:"purpose"`
	Algorithm         string  `json:"algorithm"`
	PublicKey         string  `json:"public_key"`
	CreatedAt         int64   `json:"created_at"`
	NotAfter          *int64  `json:"not_after"`
	PreviousKeyID     *string `json:"previous_key_id"`
	RotationSignature string  `json:"rotation_signature,omitempty"`
	BindingSignature  string  `json:"binding_signature,omitempty"`
	RevokedAt         *int64  `json:"revoked_at"`
}

type InitialIdentityKey struct {
	ID        string `json:"id"`
	Algorithm string `json:"algorithm"`
	PublicKey string `json:"public_key"`
	CreatedAt int64  `json:"created_at"`
}

type SuccessorIdentityKey struct {
	ID        string `json:"id"`
	Algorithm string `json:"algorithm"`
	PublicKey string `json:"public_key"`
	CreatedAt int64  `json:"created_at"`
	NotAfter  *int64 `json:"not_after"`
}

type Identity struct {
	Protocol            string        `json:"protocol,omitempty"`
	ID                  string        `json:"id"`
	Handle              string        `json:"handle"`
	Address             string        `json:"address"`
	DisplayName         string        `json:"display_name"`
	State               string        `json:"state"`
	Fingerprint         string        `json:"fingerprint"`
	EncryptionKey       *IdentityKey  `json:"encryption_key,omitempty"`
	SigningKey          *IdentityKey  `json:"signing_key,omitempty"`
	ActiveEncryptionKey *IdentityKey  `json:"active_encryption_key,omitempty"`
	ActiveSigningKey    *IdentityKey  `json:"active_signing_key,omitempty"`
	Keys                []IdentityKey `json:"keys,omitempty"`
	CreatedAt           int64         `json:"created_at"`
	DisabledAt          *int64        `json:"disabled_at"`
}

type IdentityList struct {
	Identities []Identity `json:"identities"`
}
type IdentityEnvelope struct {
	Identity Identity `json:"identity"`
}

type RecipientPin struct {
	IdentityID  string `json:"identity_id"`
	KeyID       string `json:"key_id"`
	LocalAlias  string `json:"local_alias"`
	Fingerprint string `json:"fingerprint"`
	TrustState  string `json:"trust_state"`
}
type RecipientPinEnvelope struct {
	Recipient RecipientPin `json:"recipient"`
}
type RecipientPinList struct {
	Recipients []RecipientPin `json:"recipients"`
}

type IdentityChallenge struct {
	ID         string `json:"id"`
	IdentityID string `json:"identity_id"`
	Challenge  string `json:"challenge"`
	Operation  string `json:"operation"`
	IssuedAt   int64  `json:"issued_at"`
	ExpiresAt  int64  `json:"expires_at"`
}

type Delivery struct {
	ID                     string  `json:"id"`
	TransferID             string  `json:"transfer_id"`
	State                  string  `json:"state"`
	SenderIdentityID       *string `json:"sender_identity_id"`
	SenderSigningKeyID     *string `json:"sender_signing_key_id"`
	RecipientIdentityID    string  `json:"recipient_identity_id"`
	RecipientKeyID         string  `json:"recipient_key_id"`
	WrappedMasterKey       string  `json:"wrapped_master_key,omitempty"`
	ManifestSHA256         string  `json:"manifest_sha256"`
	AlsoLink               bool    `json:"also_link"`
	DeclaredPlaintextBytes int64   `json:"declared_plaintext_bytes,omitempty"`
	CiphertextBytes        int64   `json:"ciphertext_bytes,omitempty"`
	CreatedAt              int64   `json:"created_at"`
	OfferedAt              *int64  `json:"offered_at"`
	ExpiresAt              int64   `json:"expires_at"`
}

type DeliveryEnvelope struct {
	Delivery Delivery `json:"delivery"`
}
type IdentityKeyEnvelope struct {
	Key IdentityKey `json:"key"`
}

type InboxList struct {
	Deliveries []Delivery `json:"deliveries"`
	NextCursor *string    `json:"next_cursor"`
}

type InboxClaim struct {
	ClaimID             string       `json:"claim_id"`
	ClaimNonce          string       `json:"claim_nonce"`
	LeaseExpiresAt      int64        `json:"lease_expires_at"`
	Delivery            Delivery     `json:"delivery"`
	Transfer            Transfer     `json:"transfer"`
	SenderSigningKey    *IdentityKey `json:"sender_signing_key"`
	SenderIdentityState *string      `json:"sender_identity_state"`
}

type SignedDeliveryReceipt struct {
	Protocol            string `json:"protocol"`
	ReceiptID           string `json:"receipt_id"`
	DeliveryID          string `json:"delivery_id"`
	TransferID          string `json:"transfer_id"`
	Type                string `json:"type"`
	RecipientIdentityID string `json:"recipient_identity_id"`
	SigningKeyID        string `json:"signing_key_id"`
	ClaimID             string `json:"claim_id"`
	ClaimNonce          string `json:"claim_nonce"`
	ManifestSHA256      string `json:"manifest_sha256"`
	EventAt             int64  `json:"event_at"`
}

type DeliveryReceiptEvent struct {
	Receipt   SignedDeliveryReceipt `json:"receipt"`
	Signature string                `json:"signature"`
}

type DeliveryReceiptView struct {
	ID             string  `json:"id"`
	DeliveryID     string  `json:"delivery_id"`
	Type           string  `json:"type"`
	ActorKind      string  `json:"actor_kind"`
	SignerKeyID    *string `json:"signer_key_id"`
	ManifestSHA256 string  `json:"manifest_sha256"`
	EventAt        int64   `json:"event_at"`
	ReceivedAt     int64   `json:"received_at"`
	Signature      *string `json:"signature"`
}
type DeliveryReceiptEnvelope struct {
	Receipt DeliveryReceiptView `json:"receipt"`
}

type DeliveryReceiptList struct {
	Receipts []DeliveryReceiptView `json:"receipts"`
}
