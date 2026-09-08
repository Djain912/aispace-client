package cmd

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aispace-sh/aispace-client/internal/api"
	"github.com/aispace-sh/aispace-client/internal/config"
	identitypkg "github.com/aispace-sh/aispace-client/internal/identity"
	"github.com/aispace-sh/aispace-client/internal/sealed"
)

func TestVerifyManifestSenderRequiresPinnedKeyBytes(t *testing.T) {
	isolate(t)
	pinnedKeys, err := identitypkg.GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	replacementKeys, err := identitypkg.GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	const identityID = "01HZZZZZZZZZZZZZZZZZZZZZZZ"
	const signingKeyID = "01HZZZZZZZZZZZZZZZZZZZZZZS"
	activeSigningPublic := identitypkg.PublicKeyString(pinnedKeys.SigningPublic)
	liveLookups := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		liveLookups++
		if r.Method != http.MethodGet || r.URL.Path != "/i/"+identityID+"/keys/"+signingKeyID {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"key": api.IdentityKey{
			Protocol: "aispace-identity-v1", IdentityID: identityID, KeyID: signingKeyID,
			Purpose: "signing", Algorithm: identitypkg.SigningAlgorithm, PublicKey: activeSigningPublic,
		}})
	}))
	defer srv.Close()
	writeConfig(t, config.File{Key: "ask_validkey0000000000000000000000", URL: srv.URL})
	encKeys, err := identitypkg.GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	pinned := identitypkg.Recipient{
		Alias: "sender", ServerURL: srv.URL, IdentityID: identityID,
		EncryptionKeyID: "01HZZZZZZZZZZZZZZZZZZZZZZE", EncryptionPublicKey: identitypkg.PublicKeyString(encKeys.EncryptionPublic),
		SigningKeyID: signingKeyID, SigningPublicKey: identitypkg.PublicKeyString(pinnedKeys.SigningPublic),
		TrustState: "pinned", CreatedAt: 1, UpdatedAt: 1,
	}
	pinned.Fingerprint = identitypkg.Fingerprint(pinned.IdentityID, pinned.EncryptionKeyID, encKeys.EncryptionPublic, pinned.SigningKeyID, pinnedKeys.SigningPublic)
	if err := identitypkg.NewStore(mustConfigPath(t)).PutRecipient(pinned); err != nil {
		t.Fatal(err)
	}
	client := api.New(srv.URL, "ask_validkey0000000000000000000000", "test")
	a := &app{}
	active := "active"

	manifest := signedTrustTestManifest(t, identityID, signingKeyID, pinnedKeys.SigningPrivate)
	snapshot := &api.IdentityKey{IdentityID: identityID, KeyID: signingKeyID, Purpose: "signing", Algorithm: identitypkg.SigningAlgorithm, PublicKey: activeSigningPublic}
	status, err := a.verifyManifestSenderWithClaim(context.Background(), client, manifest, snapshot, &active)
	if err != nil || status != "sender (verified sender)" {
		t.Fatalf("matching pin = %q, %v", status, err)
	}
	revokedAt := time.Now().Unix()
	revokedSnapshot := *snapshot
	revokedSnapshot.RevokedAt = &revokedAt
	status, err = a.verifyManifestSenderWithClaim(context.Background(), client, manifest, &revokedSnapshot, &active)
	if err != nil || !strings.Contains(status, "revoked") || strings.Contains(status, "verified sender") {
		t.Fatalf("revoked snapshot = %q, %v", status, err)
	}
	notAfter := time.Now().Add(-time.Minute).Unix()
	expiredSnapshot := *snapshot
	expiredSnapshot.NotAfter = &notAfter
	status, err = a.verifyManifestSenderWithClaim(context.Background(), client, manifest, &expiredSnapshot, &active)
	if err != nil || !strings.Contains(status, "expired") || strings.Contains(status, "verified sender") {
		t.Fatalf("expired snapshot = %q, %v", status, err)
	}
	disabled := "disabled"
	status, err = a.verifyManifestSenderWithClaim(context.Background(), client, manifest, snapshot, &disabled)
	if err != nil || !strings.Contains(status, "disabled") || strings.Contains(status, "verified sender") {
		t.Fatalf("disabled identity = %q, %v", status, err)
	}
	replacementSnapshot := &api.IdentityKey{IdentityID: identityID, KeyID: signingKeyID, Purpose: "signing", Algorithm: identitypkg.SigningAlgorithm, PublicKey: identitypkg.PublicKeyString(replacementKeys.SigningPublic)}
	status, err = a.verifyManifestSenderWithClaim(context.Background(), client, manifest, replacementSnapshot, &active)
	if err == nil || status != "" || !strings.Contains(err.Error(), "signature verification") {
		t.Fatalf("same-ID claim snapshot replacement = %q, %v", status, err)
	}

	activeSigningPublic = identitypkg.PublicKeyString(replacementKeys.SigningPublic)
	manifest = signedTrustTestManifest(t, identityID, signingKeyID, replacementKeys.SigningPrivate)
	replacementSnapshot.PublicKey = activeSigningPublic
	status, err = a.verifyManifestSenderWithClaim(context.Background(), client, manifest, replacementSnapshot, &active)
	if err == nil || status != "" || !strings.Contains(err.Error(), "differ from the locally pinned key") {
		t.Fatalf("same-ID replacement = %q, %v", status, err)
	}
	if liveLookups != 0 {
		t.Fatalf("historical sender verification performed %d live lookups", liveLookups)
	}
}

func signedTrustTestManifest(t *testing.T, identityID, signingKeyID string, private []byte) sealed.Manifest {
	t.Helper()
	manifest := sealed.Manifest{
		Protocol: sealed.Protocol, TransferID: "01HZZZZZZZZZZZZZZZZZZZZZZT", CreatedAt: 1, ExpiresAt: 2,
		CiphertextSHA256: strings.Repeat("0", 64),
		Files:            []sealed.ManifestFile{{Index: 0, Path: "empty", SizeBytes: 0, SHA256: "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855", Chunks: []sealed.ManifestChunk{{GlobalIndex: 0, PlaintextBytes: 0}}}},
		Sender:           &sealed.ManifestSender{IdentityID: identityID, SigningKeyID: signingKeyID},
		Signature:        &sealed.ManifestSignature{Algorithm: identitypkg.SigningAlgorithm, Value: "pending"},
	}
	unsigned, err := sealed.CanonicalUnsignedManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	signature, err := identitypkg.SignCanonical(private, identitypkg.ManifestSignatureDomain, unsigned)
	if err != nil {
		t.Fatal(err)
	}
	manifest.Signature.Value = base64.RawURLEncoding.EncodeToString(signature)
	return manifest
}

func TestInboxManifestBindingRejectsEveryAuthenticatedEnvelopeMismatch(t *testing.T) {
	manifest, claim := inboxBindingFixture()
	if err := validateInboxManifestEnvelope(claim, strings.Repeat("a", 64), 99); err != nil {
		t.Fatal(err)
	}
	if err := validateInboxManifestBinding(manifest, claim, "recipient"); err != nil {
		t.Fatal(err)
	}
	tests := map[string]func(*sealed.Manifest, *api.InboxClaim){
		"transfer id": func(m *sealed.Manifest, _ *api.InboxClaim) { m.TransferID = "other" },
		"created at":  func(m *sealed.Manifest, _ *api.InboxClaim) { m.CreatedAt++ },
		"expires at":  func(m *sealed.Manifest, _ *api.InboxClaim) { m.Delivery.ExpiresAt++ },
		"file count":  func(_ *sealed.Manifest, c *api.InboxClaim) { c.Transfer.FileCount++ },
		"plaintext":   func(_ *sealed.Manifest, c *api.InboxClaim) { c.Transfer.DeclaredPlaintextBytes++ },
		"ciphertext":  func(_ *sealed.Manifest, c *api.InboxClaim) { c.Transfer.CiphertextBytes++ },
		"cipher hash": func(_ *sealed.Manifest, c *api.InboxClaim) { *c.Transfer.CiphertextSHA256 = strings.Repeat("b", 64) },
		"max downloads": func(m *sealed.Manifest, _ *api.InboxClaim) {
			*m.Delivery.MaxDownloads = 2
		},
		"also link":     func(m *sealed.Manifest, _ *api.InboxClaim) { m.Delivery.AlsoLink = true },
		"recipient id":  func(m *sealed.Manifest, _ *api.InboxClaim) { m.Delivery.RecipientIdentityID = "other" },
		"recipient key": func(_ *sealed.Manifest, c *api.InboxClaim) { c.Delivery.RecipientKeyID = "other" },
		"sender id":     func(m *sealed.Manifest, _ *api.InboxClaim) { *m.Delivery.SenderIdentityID = "other" },
		"sender key":    func(_ *sealed.Manifest, c *api.InboxClaim) { *c.Delivery.SenderSigningKeyID = "other" },
		"sender snapshot": func(_ *sealed.Manifest, c *api.InboxClaim) {
			c.SenderSigningKey.PublicKey = "replacement"
			c.SenderSigningKey.KeyID = "other"
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			m, c := inboxBindingFixture()
			mutate(&m, &c)
			if err := validateInboxManifestBinding(m, c, "recipient"); err == nil {
				t.Fatal("mismatch accepted")
			}
		})
	}
	badEnvelope := claim
	*badEnvelope.Transfer.ManifestSHA256 = strings.Repeat("b", 64)
	if err := validateInboxManifestEnvelope(badEnvelope, strings.Repeat("a", 64), 99); err == nil {
		t.Fatal("manifest digest mismatch accepted")
	}
	manifest, claim = inboxBindingFixture()
	*claim.Transfer.ManifestSizeBytes = 100
	if err := validateInboxManifestEnvelope(claim, strings.Repeat("a", 64), 99); err == nil {
		t.Fatal("manifest size mismatch accepted")
	}
}

func inboxBindingFixture() (sealed.Manifest, api.InboxClaim) {
	manifestMaxDownloads, claimMaxDownloads := int64(1), int64(1)
	manifestSenderID, manifestSenderKeyID := "sender", "sender-key"
	claimSenderID, claimSenderKeyID := "sender", "sender-key"
	claimSenderState := "active"
	manifestSHA, ciphertextSHA := strings.Repeat("a", 64), strings.Repeat("0", 64)
	manifestSize := int64(99)
	manifest := sealed.Manifest{
		Protocol: sealed.Protocol, TransferID: "transfer", CreatedAt: 10, ExpiresAt: 20, CiphertextSHA256: ciphertextSHA,
		Files:     []sealed.ManifestFile{{Index: 0, Path: "empty", SizeBytes: 0, SHA256: "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855", Chunks: []sealed.ManifestChunk{{GlobalIndex: 0, PlaintextBytes: 0}}}},
		Sender:    &sealed.ManifestSender{IdentityID: manifestSenderID, SigningKeyID: manifestSenderKeyID},
		Recipient: &sealed.ManifestRecipient{IdentityID: "recipient", EncryptionKeyID: "recipient-key"},
		Delivery: &sealed.ManifestDelivery{
			Mode: "addressed", AlsoLink: false, MaxDownloads: &manifestMaxDownloads,
			RecipientIdentityID: "recipient", RecipientKeyID: "recipient-key", SenderIdentityID: &manifestSenderID, SenderSigningKeyID: &manifestSenderKeyID,
			CreatedAt: 10, ExpiresAt: 20, FileCount: 1, DeclaredPlaintextBytes: 0,
		},
		Signature: &sealed.ManifestSignature{Algorithm: identitypkg.SigningAlgorithm, Value: "signature"},
	}
	claim := api.InboxClaim{
		Delivery:            api.Delivery{ID: "delivery", TransferID: "transfer", SenderIdentityID: &claimSenderID, SenderSigningKeyID: &claimSenderKeyID, RecipientIdentityID: "recipient", RecipientKeyID: "recipient-key", ManifestSHA256: manifestSHA, AlsoLink: false, ExpiresAt: 20},
		Transfer:            api.Transfer{ID: "transfer", CreatedAt: 10, ExpiresAt: 20, FileCount: 1, DeclaredPlaintextBytes: 0, CiphertextBytes: 16, ManifestSizeBytes: &manifestSize, ManifestSHA256: &manifestSHA, CiphertextSHA256: &ciphertextSHA, MaxDownloads: &claimMaxDownloads},
		SenderSigningKey:    &api.IdentityKey{IdentityID: "sender", KeyID: "sender-key", Purpose: "signing", Algorithm: identitypkg.SigningAlgorithm, PublicKey: "snapshot"},
		SenderIdentityState: &claimSenderState,
	}
	return manifest, claim
}
