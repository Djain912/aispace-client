package cmd

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aispace-sh/aispace-client/internal/api"
	"github.com/aispace-sh/aispace-client/internal/config"
	identitypkg "github.com/aispace-sh/aispace-client/internal/identity"
)

func TestIdentityCreateStoresPrivateKeysButUploadsOnlyPublicMaterial(t *testing.T) {
	isolate(t)
	var createBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/identity-challenges":
			w.WriteHeader(201)
			_, _ = io.WriteString(w, `{"id":"01CHALLENGE","identity_id":"01IDENTITY","challenge":"Y2hhbGxlbmdl","operation":"identity.create","issued_at":1788881400,"expires_at":1788881700}`)
		case "/v1/identities":
			createBody, _ = io.ReadAll(r.Body)
			var in map[string]any
			_ = json.Unmarshal(createBody, &in)
			enc := in["encryption_key"].(map[string]any)
			sign := in["signing_key"].(map[string]any)
			encRaw, _ := identitypkg.ParsePublicKey(enc["public_key"].(string), 32)
			signRaw, _ := identitypkg.ParsePublicKey(sign["public_key"].(string), 32)
			fp := identitypkg.Fingerprint("01IDENTITY", enc["id"].(string), encRaw, sign["id"].(string), signRaw)
			w.WriteHeader(201)
			_ = json.NewEncoder(w).Encode(map[string]any{"identity": map[string]any{"id": "01IDENTITY", "handle": "research-agent", "display_name": "Research agent", "state": "active", "fingerprint": fp, "created_at": 1788881400}})
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	writeConfig(t, config.File{Key: "ask_validkey0000000000000000000000", URL: srv.URL})
	r := run("", "identity", "create", "--name", "Research agent", "--handle", "research-agent")
	if r.code != ExitOK {
		t.Fatalf("%+v", r)
	}
	if strings.Contains(string(createBody), "private") {
		t.Fatalf("private key leaked: %s", createBody)
	}
	var in map[string]any
	if err := json.Unmarshal(createBody, &in); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"challenge", "possession_signature", "encryption_binding_signature"} {
		if in[field] == nil {
			t.Fatalf("missing %s in %s", field, createBody)
		}
	}
	path := filepath.Join(filepath.Dir(mustConfigPath(t)), "identities", "01IDENTITY.json")
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if permBits && st.Mode().Perm() != 0o600 {
		t.Fatalf("mode %04o", st.Mode().Perm())
	}
	local, err := identitypkg.NewStore(mustConfigPath(t)).LoadIdentity("01IDENTITY")
	if err != nil {
		t.Fatal(err)
	}
	if local.EncryptionPrivateKey == "" || local.SigningPrivateKey == "" {
		t.Fatal("private keys missing")
	}
}

func TestAddressedTransferRequiresExplicitTrust(t *testing.T) {
	isolate(t)
	writeConfig(t, config.File{Key: "ask_validkey0000000000000000000000", URL: "http://localhost:1"})
	keys, _ := identitypkg.GenerateKeyPair()
	recipient := identitypkg.Recipient{Alias: "bot", ServerURL: "http://localhost:1", IdentityID: "I", EncryptionKeyID: "E", EncryptionPublicKey: identitypkg.PublicKeyString(keys.EncryptionPublic), SigningKeyID: "S", SigningPublicKey: identitypkg.PublicKeyString(keys.SigningPublic), Fingerprint: identitypkg.Fingerprint("I", "E", keys.EncryptionPublic, "S", keys.SigningPublic), TrustState: "unverified", CreatedAt: 1, UpdatedAt: 1}
	store := identitypkg.NewStore(mustConfigPath(t))
	if err := store.PutRecipient(recipient); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(t.TempDir(), "file.txt")
	if err := os.WriteFile(src, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	r := run("", "transfer", "create", src, "--to", "bot")
	if r.code == ExitOK || !strings.Contains(r.stderr, "recipient") {
		t.Fatalf("unverified send not blocked: %+v", r)
	}
}

func mustConfigPath(t *testing.T) string {
	t.Helper()
	path, err := config.Path()
	if err != nil {
		t.Fatal(err)
	}
	return path
}

func TestValidRecipientRotationRequiresPredecessorSignature(t *testing.T) {
	oldKeys, _ := identitypkg.GenerateKeyPair()
	newKeys, _ := identitypkg.GenerateKeyPair()
	old := identitypkg.Recipient{Alias: "bot", IdentityID: "I", EncryptionKeyID: "E1", EncryptionPublicKey: identitypkg.PublicKeyString(oldKeys.EncryptionPublic), SigningKeyID: "S1", SigningPublicKey: identitypkg.PublicKeyString(oldKeys.SigningPublic), TrustState: "pinned"}
	previous := "E1"
	key := api.IdentityKey{IdentityID: "I", KeyID: "E2", Purpose: "encryption", Algorithm: "X25519", PublicKey: identitypkg.PublicKeyString(newKeys.EncryptionPublic), CreatedAt: 20, PreviousKeyID: &previous}
	statement := struct {
		Algorithm        string `json:"algorithm"`
		IdentityID       string `json:"identity_id"`
		NotAfter         *int64 `json:"not_after"`
		PredecessorKeyID string `json:"predecessor_key_id"`
		PublicKey        string `json:"public_key"`
		Purpose          string `json:"purpose"`
		SuccessorKeyID   string `json:"successor_key_id"`
		ValidFrom        int64  `json:"valid_from"`
	}{key.Algorithm, "I", nil, "E1", key.PublicKey, "encryption", "E2", 20}
	canonical, _ := identitypkg.CanonicalJSON(statement)
	sig, _ := identitypkg.SignCanonical(oldKeys.SigningPrivate, identitypkg.SuccessorSignatureDomain, canonical)
	key.RotationSignature = base64.RawURLEncoding.EncodeToString(sig)
	sign := api.IdentityKey{IdentityID: "I", KeyID: "S1", Purpose: "signing", Algorithm: "Ed25519", PublicKey: identitypkg.PublicKeyString(oldKeys.SigningPublic)}
	current := old
	current.EncryptionKeyID = "E2"
	current.EncryptionPublicKey = key.PublicKey
	public := api.Identity{ID: "I", ActiveEncryptionKey: &key, ActiveSigningKey: &sign}
	fetch := func(keyID string) (api.IdentityKey, error) {
		return api.IdentityKey{IdentityID: "I", KeyID: keyID, Purpose: "encryption", Algorithm: identitypkg.EncryptionAlgorithm, PublicKey: old.EncryptionPublicKey}, nil
	}
	if ok, err := validRecipientRotation(old, current, public, fetch); err != nil || !ok {
		t.Fatal("valid successor rejected")
	}
	maliciousKeys, _ := identitypkg.GenerateKeyPair()
	current.SigningPublicKey = identitypkg.PublicKeyString(maliciousKeys.SigningPublic)
	sign.PublicKey = current.SigningPublicKey
	if ok, _ := validRecipientRotation(old, current, public, fetch); ok {
		t.Fatal("encryption rotation accepted a replacement signing key under the pinned key ID")
	}
	current.SigningPublicKey = old.SigningPublicKey
	sign.PublicKey = old.SigningPublicKey
	key.RotationSignature = base64.RawURLEncoding.EncodeToString(ed25519.Sign(newKeys.SigningPrivate, canonical))
	if ok, _ := validRecipientRotation(old, current, public, fetch); ok {
		t.Fatal("unrelated successor accepted")
	}
}

func TestValidSigningRotationRequiresUnchangedEncryptionKeyBytes(t *testing.T) {
	oldKeys, _ := identitypkg.GenerateKeyPair()
	newKeys, _ := identitypkg.GenerateKeyPair()
	old := identitypkg.Recipient{Alias: "bot", IdentityID: "I", EncryptionKeyID: "E1", EncryptionPublicKey: identitypkg.PublicKeyString(oldKeys.EncryptionPublic), SigningKeyID: "S1", SigningPublicKey: identitypkg.PublicKeyString(oldKeys.SigningPublic), TrustState: "pinned"}
	previous := "S1"
	successor := api.IdentityKey{IdentityID: "I", KeyID: "S2", Purpose: "signing", Algorithm: "Ed25519", PublicKey: identitypkg.PublicKeyString(newKeys.SigningPublic), CreatedAt: 20, PreviousKeyID: &previous}
	statement := struct {
		Algorithm        string `json:"algorithm"`
		IdentityID       string `json:"identity_id"`
		NotAfter         *int64 `json:"not_after"`
		PredecessorKeyID string `json:"predecessor_key_id"`
		PublicKey        string `json:"public_key"`
		Purpose          string `json:"purpose"`
		SuccessorKeyID   string `json:"successor_key_id"`
		ValidFrom        int64  `json:"valid_from"`
	}{successor.Algorithm, "I", nil, "S1", successor.PublicKey, "signing", "S2", 20}
	canonical, _ := identitypkg.CanonicalJSON(statement)
	sig, _ := identitypkg.SignCanonical(oldKeys.SigningPrivate, identitypkg.SuccessorSignatureDomain, canonical)
	successor.RotationSignature = base64.RawURLEncoding.EncodeToString(sig)
	enc := api.IdentityKey{IdentityID: "I", KeyID: "E1", Purpose: "encryption", Algorithm: "X25519", PublicKey: old.EncryptionPublicKey}
	current := old
	current.SigningKeyID = "S2"
	current.SigningPublicKey = successor.PublicKey
	public := api.Identity{ID: "I", ActiveEncryptionKey: &enc, ActiveSigningKey: &successor}
	fetch := func(keyID string) (api.IdentityKey, error) {
		return api.IdentityKey{IdentityID: "I", KeyID: keyID, Purpose: "signing", Algorithm: identitypkg.SigningAlgorithm, PublicKey: old.SigningPublicKey}, nil
	}
	if ok, err := validRecipientRotation(old, current, public, fetch); err != nil || !ok {
		t.Fatal("valid signing successor rejected")
	}
	enc.PublicKey = identitypkg.PublicKeyString(newKeys.EncryptionPublic)
	current.EncryptionPublicKey = enc.PublicKey
	if ok, _ := validRecipientRotation(old, current, public, fetch); ok {
		t.Fatal("signing rotation accepted a replacement encryption key under the pinned key ID")
	}
}

func TestRecipientRotationWalksTwoHopDualPurposeHistory(t *testing.T) {
	e1, _ := identitypkg.GenerateKeyPair()
	e2, _ := identitypkg.GenerateKeyPair()
	e3, _ := identitypkg.GenerateKeyPair()
	s1, _ := identitypkg.GenerateKeyPair()
	s2, _ := identitypkg.GenerateKeyPair()
	s3, _ := identitypkg.GenerateKeyPair()
	s1Record := api.IdentityKey{IdentityID: "I", KeyID: "S1", Purpose: "signing", Algorithm: identitypkg.SigningAlgorithm, PublicKey: identitypkg.PublicKeyString(s1.SigningPublic), CreatedAt: 1}
	s2Record := signedSuccessor(t, "I", "signing", "S2", "S1", identitypkg.SigningAlgorithm, identitypkg.PublicKeyString(s2.SigningPublic), 20, s1.SigningPrivate)
	s3Record := signedSuccessor(t, "I", "signing", "S3", "S2", identitypkg.SigningAlgorithm, identitypkg.PublicKeyString(s3.SigningPublic), 40, s2.SigningPrivate)
	e1Record := api.IdentityKey{IdentityID: "I", KeyID: "E1", Purpose: "encryption", Algorithm: identitypkg.EncryptionAlgorithm, PublicKey: identitypkg.PublicKeyString(e1.EncryptionPublic), CreatedAt: 1}
	e2Record := signedSuccessor(t, "I", "encryption", "E2", "E1", identitypkg.EncryptionAlgorithm, identitypkg.PublicKeyString(e2.EncryptionPublic), 10, s1.SigningPrivate)
	e3Record := signedSuccessor(t, "I", "encryption", "E3", "E2", identitypkg.EncryptionAlgorithm, identitypkg.PublicKeyString(e3.EncryptionPublic), 30, s2.SigningPrivate)
	records := map[string]api.IdentityKey{"S1": s1Record, "S2": s2Record, "E1": e1Record, "E2": e2Record}
	fetch := func(keyID string) (api.IdentityKey, error) { return records[keyID], nil }
	old := identitypkg.Recipient{IdentityID: "I", EncryptionKeyID: "E1", EncryptionPublicKey: e1Record.PublicKey, SigningKeyID: "S1", SigningPublicKey: s1Record.PublicKey}
	current := identitypkg.Recipient{IdentityID: "I", EncryptionKeyID: "E3", EncryptionPublicKey: e3Record.PublicKey, SigningKeyID: "S3", SigningPublicKey: s3Record.PublicKey}
	public := api.Identity{ID: "I", ActiveEncryptionKey: &e3Record, ActiveSigningKey: &s3Record}
	if ok, err := validRecipientRotation(old, current, public, fetch); err != nil || !ok {
		t.Fatalf("valid two-hop dual-purpose history rejected: %v", err)
	}

	replacement, _ := identitypkg.GenerateKeyPair()
	tampered := records["E2"]
	tampered.PublicKey = identitypkg.PublicKeyString(replacement.EncryptionPublic)
	records["E2"] = tampered
	if ok, _ := validRecipientRotation(old, current, public, fetch); ok {
		t.Fatal("same-ID historical key-byte replacement accepted")
	}
}

func signedSuccessor(t *testing.T, identityID, purpose, keyID, predecessorID, algorithm, publicKey string, createdAt int64, signer ed25519.PrivateKey) api.IdentityKey {
	t.Helper()
	previous := predecessorID
	key := api.IdentityKey{IdentityID: identityID, KeyID: keyID, Purpose: purpose, Algorithm: algorithm, PublicKey: publicKey, CreatedAt: createdAt, PreviousKeyID: &previous}
	statement := struct {
		Algorithm        string `json:"algorithm"`
		IdentityID       string `json:"identity_id"`
		NotAfter         *int64 `json:"not_after"`
		PredecessorKeyID string `json:"predecessor_key_id"`
		PublicKey        string `json:"public_key"`
		Purpose          string `json:"purpose"`
		SuccessorKeyID   string `json:"successor_key_id"`
		ValidFrom        int64  `json:"valid_from"`
	}{algorithm, identityID, nil, predecessorID, publicKey, purpose, keyID, createdAt}
	canonical, err := identitypkg.CanonicalJSON(statement)
	if err != nil {
		t.Fatal(err)
	}
	signature, err := identitypkg.SignCanonical(signer, identitypkg.SuccessorSignatureDomain, canonical)
	if err != nil {
		t.Fatal(err)
	}
	key.RotationSignature = base64.RawURLEncoding.EncodeToString(signature)
	return key
}

func TestRecipientRemoveDeletesServerBeforeLocalAndAcceptsAbsentServerPin(t *testing.T) {
	isolate(t)
	const identityID = "01HZZZZZZZZZZZZZZZZZZZZZZZ"
	deleteCalls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete || r.URL.Path != "/v1/recipient-pins/"+identityID {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		deleteCalls++
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	writeConfig(t, config.File{Key: "ask_validkey0000000000000000000000", URL: srv.URL})
	store := identitypkg.NewStore(mustConfigPath(t))
	recipient := testRecipient(t, srv.URL, "bot", identityID)

	for i := 0; i < 2; i++ {
		if err := store.PutRecipient(recipient); err != nil {
			t.Fatal(err)
		}
		r := run("", "recipient", "remove", "bot")
		if r.code != ExitOK {
			t.Fatalf("remove %d: %+v", i+1, r)
		}
		if _, err := store.Recipient(identityID); err == nil {
			t.Fatal("local recipient remained after successful server deletion")
		}
	}
	if deleteCalls != 2 {
		t.Fatalf("got %d DELETE requests", deleteCalls)
	}
}

func TestRecipientRemovePreservesLocalTrustOnServerFailure(t *testing.T) {
	isolate(t)
	const identityID = "01HZZZZZZZZZZZZZZZZZZZZZZZ"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(w, `{"error":{"code":"unavailable","message":"try later"}}`)
	}))
	defer srv.Close()
	writeConfig(t, config.File{Key: "ask_validkey0000000000000000000000", URL: srv.URL})
	store := identitypkg.NewStore(mustConfigPath(t))
	if err := store.PutRecipient(testRecipient(t, srv.URL, "bot", identityID)); err != nil {
		t.Fatal(err)
	}
	r := run("", "recipient", "remove", "bot")
	if r.code == ExitOK {
		t.Fatalf("server failure reported success: %+v", r)
	}
	if _, err := store.Recipient(identityID); err != nil {
		t.Fatalf("local trust entry was removed after server failure: %v", err)
	}
}

func testRecipient(t *testing.T, serverURL, alias, identityID string) identitypkg.Recipient {
	t.Helper()
	keys, err := identitypkg.GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	return identitypkg.Recipient{
		Alias: alias, ServerURL: serverURL, IdentityID: identityID,
		EncryptionKeyID: "01HZZZZZZZZZZZZZZZZZZZZZZE", EncryptionPublicKey: identitypkg.PublicKeyString(keys.EncryptionPublic),
		SigningKeyID: "01HZZZZZZZZZZZZZZZZZZZZZZS", SigningPublicKey: identitypkg.PublicKeyString(keys.SigningPublic),
		Fingerprint: identitypkg.Fingerprint(identityID, "01HZZZZZZZZZZZZZZZZZZZZZZE", keys.EncryptionPublic, "01HZZZZZZZZZZZZZZZZZZZZZZS", keys.SigningPublic),
		TrustState:  "pinned", CreatedAt: 1, UpdatedAt: 1,
	}
}

func TestIdentityRotationRecoversPersistedRequestAndPromotesStagedKey(t *testing.T) {
	isolate(t)
	const identityID = "01ARZ3NDEKTSV4RRFFQ69G5FAV"
	const successorID = "01ARZ3NDEKTSV4RRFFQ69G5FAW"
	const idempotencyKey = "rotation-replay-key"
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Method != http.MethodPost || r.URL.Path != "/v1/identities/"+identityID+"/keys" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Idempotency-Key") != idempotencyKey {
			t.Fatalf("idempotency key %q", r.Header.Get("Idempotency-Key"))
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{"key":{"identity_id":"`+identityID+`","key_id":"`+successorID+`","purpose":"encryption","algorithm":"X25519","public_key":"key","created_at":2}}`)
	}))
	defer srv.Close()
	writeConfig(t, config.File{Key: "ask_validkey0000000000000000000000", URL: srv.URL})
	store := identitypkg.NewStore(mustConfigPath(t))
	oldKeys, _ := identitypkg.GenerateKeyPair()
	local := identitypkg.NewLocalIdentity(srv.URL, identityID, "agent", "Agent", "01ARZ3NDEKTSV4RRFFQ69G5FAX", "01ARZ3NDEKTSV4RRFFQ69G5FAY", 1, oldKeys)
	if _, err := store.SaveIdentity(local); err != nil {
		t.Fatal(err)
	}
	newKeys, _ := identitypkg.GenerateKeyPair()
	local.EncryptionKeyID = successorID
	local.EncryptionPrivateKey = base64.RawURLEncoding.EncodeToString(newKeys.EncryptionPrivate)
	local.EncryptionPublicKey = identitypkg.PublicKeyString(newKeys.EncryptionPublic)
	local.EncryptionKeys = append(local.EncryptionKeys, identitypkg.LocalPrivateKey{KeyID: successorID, PrivateKey: local.EncryptionPrivateKey, PublicKey: local.EncryptionPublicKey, CreatedAt: 2})
	staged, err := store.StageIdentity(local, successorID)
	if err != nil {
		t.Fatal(err)
	}
	request := api.RotateIdentityKeyRequest{ChallengeID: "challenge", Challenge: "raw", Purpose: "encryption", Key: api.SuccessorIdentityKey{ID: successorID, Algorithm: identitypkg.EncryptionAlgorithm, PublicKey: local.EncryptionPublicKey, CreatedAt: 2}, PredecessorKeyID: "01ARZ3NDEKTSV4RRFFQ69G5FAX", RotationSignature: "signature"}
	pending := pendingIdentityRotation{Version: 1, ServerURL: srv.URL, IdentityID: identityID, SuccessorKeyID: successorID, Purpose: "encryption", StagedPath: staged, IdempotencyKey: idempotencyKey, Request: request}
	if _, err := store.SavePending("rotation", identityID, pending); err != nil {
		t.Fatal(err)
	}
	r := run("", "identity", "rotate", identityID, "--purpose", "encryption")
	if r.code != ExitOK {
		t.Fatalf("rotation replay: %+v", r)
	}
	got, err := store.LoadIdentity(identityID)
	if err != nil || got.EncryptionKeyID != successorID {
		t.Fatalf("promoted identity key %q: %v", got.EncryptionKeyID, err)
	}
	var leftover pendingIdentityRotation
	if err := store.LoadPending("rotation", identityID, &leftover); !os.IsNotExist(err) {
		t.Fatalf("pending replay record remained: %v", err)
	}
	if requests != 1 {
		t.Fatalf("got %d publish requests", requests)
	}
}

func TestIdentityCreateRecoversPersistedPublication(t *testing.T) {
	isolate(t)
	const identityID = "01ARZ3NDEKTSV4RRFFQ69G5FAV"
	const idempotencyKey = "identity-create-replay"
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Method != http.MethodPost || r.URL.Path != "/v1/identities" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Idempotency-Key") != idempotencyKey {
			t.Fatalf("idempotency key %q", r.Header.Get("Idempotency-Key"))
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{"identity":{"id":"`+identityID+`","handle":"agent","display_name":"Agent","state":"active","fingerprint":"`+strings.Repeat("A", 64)+`"}}`)
	}))
	defer srv.Close()
	writeConfig(t, config.File{Key: "ask_validkey0000000000000000000000", URL: srv.URL})
	store := identitypkg.NewStore(mustConfigPath(t))
	keys, _ := identitypkg.GenerateKeyPair()
	local := identitypkg.NewLocalIdentity(srv.URL, identityID, "agent", "Agent", "enc", "sig", 1, keys)
	keyPath, err := store.SaveIdentity(local)
	if err != nil {
		t.Fatal(err)
	}
	request := api.CreateIdentityRequest{IdentityID: identityID, Handle: "agent", DisplayName: "Agent", EncryptionKey: api.InitialIdentityKey{ID: "enc", PublicKey: local.EncryptionPublicKey}, SigningKey: api.InitialIdentityKey{ID: "sig", PublicKey: local.SigningPublicKey}}
	pending := pendingIdentityCreate{Version: 1, ServerURL: srv.URL, Handle: "agent", DisplayName: "Agent", ChallengeIdempotencyKey: "challenge-replay", CreateIdempotencyKey: idempotencyKey, KeyPath: keyPath, Request: &request}
	if _, err := store.SavePending("identity-create", "agent", pending); err != nil {
		t.Fatal(err)
	}
	r := run("", "identity", "create", "--name", "Agent", "--handle", "agent")
	if r.code != ExitOK {
		t.Fatalf("create replay: %+v", r)
	}
	var leftover pendingIdentityCreate
	if err := store.LoadPending("identity-create", "agent", &leftover); !os.IsNotExist(err) {
		t.Fatalf("pending replay record remained: %v", err)
	}
	if requests != 1 {
		t.Fatalf("got %d requests", requests)
	}
}

func TestIdentityAndRecipientServerOriginsAreEnforced(t *testing.T) {
	keys, _ := identitypkg.GenerateKeyPair()
	local := identitypkg.NewLocalIdentity("https://one.example", "identity", "agent", "Agent", "enc", "sig", time.Now().Unix(), keys)
	if err := ensureLocalIdentityServer(local, "https://two.example"); err == nil {
		t.Fatal("cross-origin local identity accepted")
	}
	recipient := identitypkg.Recipient{ServerURL: "https://one.example"}
	if err := ensureRecipientServer(recipient, "https://two.example"); err == nil {
		t.Fatal("cross-origin recipient accepted")
	}

	isolate(t)
	writeConfig(t, config.File{Key: "ask_validkey0000000000000000000000", URL: "http://localhost:1"})
	r := run("", "recipient", "add", "https://other.example/i/identity#fp."+strings.Repeat("A", 64))
	if r.code == ExitOK || !strings.Contains(r.stderr, "invitation belongs to") {
		t.Fatalf("cross-origin invitation: %+v", r)
	}
}

func TestIdentityCommandsValidateInputsBeforeRemoteIdempotency(t *testing.T) {
	isolate(t)
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests++ }))
	defer srv.Close()
	writeConfig(t, config.File{Key: "ask_validkey0000000000000000000000", URL: srv.URL})
	if result := run("", "identity", "create", "--name", strings.Repeat("x", 101), "--handle", "agent"); result.code != ExitUsage {
		t.Fatalf("invalid create input: %+v", result)
	}
	if result := run("", "identity", "rotate", "identity", "--purpose", "invalid"); result.code != ExitUsage {
		t.Fatalf("invalid rotate input: %+v", result)
	}
	if requests != 0 {
		t.Fatalf("invalid local input acquired %d remote idempotency claims", requests)
	}
	store := identitypkg.NewStore(mustConfigPath(t))
	var pending pendingIdentityCreate
	if err := store.LoadPending("identity-create", "agent", &pending); !os.IsNotExist(err) {
		t.Fatalf("invalid create persisted replay state: %v", err)
	}
}

func TestClaimInboxRecoversPersistedIdempotencyAndNonce(t *testing.T) {
	isolate(t)
	const deliveryID = "01ARZ3NDEKTSV4RRFFQ69G5FAV"
	const identityID = "01ARZ3NDEKTSV4RRFFQ69G5FAW"
	const idempotencyKey = "claim-replay-key"
	const claimNonce = "recovered-secret-nonce"
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Method != http.MethodPost || r.URL.Path != "/v1/inbox/"+deliveryID+"/claims" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Idempotency-Key") != idempotencyKey {
			t.Fatalf("idempotency key %q", r.Header.Get("Idempotency-Key"))
		}
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["recipient_identity_id"] != identityID {
			t.Fatalf("recipient identity %q", body["recipient_identity_id"])
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{"claim_id":"claim","claim_nonce":"`+claimNonce+`","lease_expires_at":`+fmt.Sprint(time.Now().Add(time.Hour).Unix())+`,"delivery":{"id":"`+deliveryID+`","transfer_id":"transfer","recipient_identity_id":"`+identityID+`","recipient_key_id":"key"},"transfer":{"id":"transfer"}}`)
	}))
	defer srv.Close()
	writeConfig(t, config.File{Key: "ask_validkey0000000000000000000000", URL: srv.URL})
	store := identitypkg.NewStore(mustConfigPath(t))
	keys, _ := identitypkg.GenerateKeyPair()
	local := identitypkg.NewLocalIdentity(srv.URL, identityID, "agent", "Agent", "enc", "sig", 1, keys)
	if _, err := store.SaveIdentity(local); err != nil {
		t.Fatal(err)
	}
	pending := pendingInboxClaim{Version: 1, ServerURL: srv.URL, DeliveryID: deliveryID, RecipientIdentityID: identityID, IdempotencyKey: idempotencyKey}
	if _, err := store.SavePending("inbox-claim", deliveryID, pending); err != nil {
		t.Fatal(err)
	}
	client := api.New(srv.URL, "ask_validkey0000000000000000000000", "test")
	claim, gotIdentity, err := (&app{}).claimInbox(context.Background(), client, deliveryID, "")
	if err != nil {
		t.Fatal(err)
	}
	if claim.ClaimNonce != claimNonce || gotIdentity.IdentityID != identityID {
		t.Fatalf("claim nonce %q identity %q", claim.ClaimNonce, gotIdentity.IdentityID)
	}
	if requests != 1 {
		t.Fatalf("got %d requests", requests)
	}
}

type commandRoundTripFunc func(*http.Request) (*http.Response, error)

func (f commandRoundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestReceiptSubmissionRecoversExactSignedRequestAfterRestart(t *testing.T) {
	store := identitypkg.NewStore(filepath.Join(t.TempDir(), "config.json"))
	event := api.DeliveryReceiptEvent{Receipt: api.SignedDeliveryReceipt{Protocol: "aispace-delivery-receipt-v1", ReceiptID: "receipt-original", DeliveryID: "delivery", TransferID: "transfer", Type: "processed", RecipientIdentityID: "recipient", SigningKeyID: "signing-key", ClaimID: "claim", ClaimNonce: "nonce", ManifestSHA256: strings.Repeat("a", 64), EventAt: 10}, Signature: "original-signature"}
	requested := []pendingInboxReceiptSubmission{{Event: event, IdempotencyKey: "persisted-idem"}}
	var bodies, idempotencyKeys, claimNonces []string
	lostClient := api.New("https://example.test", "ask_owner", "test")
	lostClient.HTTP = &http.Client{Transport: commandRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(body))
		idempotencyKeys = append(idempotencyKeys, r.Header.Get("Idempotency-Key"))
		claimNonces = append(claimNonces, r.Header.Get("X-Claim-Nonce"))
		return nil, errors.New("response lost after commit")
	})}
	if _, err := submitInboxReceiptSequence(context.Background(), lostClient, store, "delivery", "processed", requested, false, nil); err == nil {
		t.Fatal("lost response unexpectedly succeeded")
	}

	replacement := event
	replacement.Receipt.ReceiptID = "must-not-be-used"
	replacement.Receipt.SigningKeyID = "new-active-key"
	replacement.Receipt.EventAt = 99
	replacement.Signature = "must-not-be-used"
	recoveredClient := api.New("https://example.test", "ask_owner", "test")
	recoveredClient.HTTP = &http.Client{Transport: commandRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(body))
		idempotencyKeys = append(idempotencyKeys, r.Header.Get("Idempotency-Key"))
		claimNonces = append(claimNonces, r.Header.Get("X-Claim-Nonce"))
		return &http.Response{StatusCode: http.StatusCreated, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"receipt":{"id":"receipt-original","delivery_id":"delivery","type":"processed","actor_kind":"identity","manifest_sha256":"` + strings.Repeat("a", 64) + `","event_at":10,"received_at":11}}`))}, nil
	})}
	result, err := submitInboxReceiptSequence(context.Background(), recoveredClient, store, "delivery", "processed", []pendingInboxReceiptSubmission{{Event: replacement, IdempotencyKey: "new-idem"}}, false, nil)
	if err != nil || result.Value.Receipt.ID != "receipt-original" {
		t.Fatalf("recover receipt: id=%q err=%v", result.Value.Receipt.ID, err)
	}
	if len(bodies) != 3 || bodies[0] != bodies[1] || bodies[1] != bodies[2] {
		t.Fatalf("receipt replay bodies changed: %q", bodies)
	}
	for i := range idempotencyKeys {
		if idempotencyKeys[i] != "persisted-idem" || claimNonces[i] != "nonce" {
			t.Fatalf("replay %d headers idempotency=%q nonce=%q", i, idempotencyKeys[i], claimNonces[i])
		}
	}
	var pending pendingInboxReceiptSequence
	if err := store.LoadPending("receipt-processed", "delivery", &pending); !os.IsNotExist(err) {
		t.Fatalf("acknowledged receipt state remained: %v", err)
	}
}
