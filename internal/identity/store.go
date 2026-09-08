package identity

import (
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

const localFileVersion = 1

type LocalIdentity struct {
	Version              int               `json:"version"`
	ServerURL            string            `json:"server_url"`
	IdentityID           string            `json:"identity_id"`
	Handle               string            `json:"handle"`
	DisplayName          string            `json:"display_name"`
	EncryptionKeyID      string            `json:"encryption_key_id"`
	EncryptionPrivateKey string            `json:"encryption_private_key"`
	EncryptionPublicKey  string            `json:"encryption_public_key"`
	SigningKeyID         string            `json:"signing_key_id"`
	SigningPrivateKey    string            `json:"signing_private_key"`
	SigningPublicKey     string            `json:"signing_public_key"`
	CreatedAt            int64             `json:"created_at"`
	EncryptionKeys       []LocalPrivateKey `json:"encryption_keys,omitempty"`
	SigningKeys          []LocalPrivateKey `json:"signing_keys,omitempty"`
}

type LocalPrivateKey struct {
	KeyID      string `json:"key_id"`
	PrivateKey string `json:"private_key"`
	PublicKey  string `json:"public_key"`
	CreatedAt  int64  `json:"created_at"`
}

func NewLocalIdentity(serverURL, identityID, handle, displayName, encryptionKeyID, signingKeyID string, createdAt int64, keys KeyPair) LocalIdentity {
	return LocalIdentity{Version: localFileVersion, ServerURL: strings.TrimRight(serverURL, "/"), IdentityID: identityID, Handle: handle, DisplayName: displayName,
		EncryptionKeyID: encryptionKeyID, EncryptionPrivateKey: rawURL.EncodeToString(keys.EncryptionPrivate), EncryptionPublicKey: rawURL.EncodeToString(keys.EncryptionPublic),
		SigningKeyID: signingKeyID, SigningPrivateKey: rawURL.EncodeToString(keys.SigningPrivate), SigningPublicKey: rawURL.EncodeToString(keys.SigningPublic), CreatedAt: createdAt,
		EncryptionKeys: []LocalPrivateKey{{KeyID: encryptionKeyID, PrivateKey: rawURL.EncodeToString(keys.EncryptionPrivate), PublicKey: rawURL.EncodeToString(keys.EncryptionPublic), CreatedAt: createdAt}},
		SigningKeys:    []LocalPrivateKey{{KeyID: signingKeyID, PrivateKey: rawURL.EncodeToString(keys.SigningPrivate), PublicKey: rawURL.EncodeToString(keys.SigningPublic), CreatedAt: createdAt}}}
}

func (l LocalIdentity) Validate() error {
	if l.Version != localFileVersion || l.IdentityID == "" || l.EncryptionKeyID == "" || l.SigningKeyID == "" {
		return errors.New("invalid local identity record")
	}
	canonicalServer, err := CanonicalServerURL(l.ServerURL)
	if err != nil || canonicalServer != l.ServerURL {
		return errors.New("local identity server URL is invalid or non-canonical")
	}
	enc, err := l.EncryptionPrivate()
	if err != nil {
		return err
	}
	encPub, err := decodeExact(l.EncryptionPublicKey, 32, "X25519 public key")
	if err != nil {
		return err
	}
	encPrivate, err := ecdh.X25519().NewPrivateKey(enc)
	if err != nil {
		return err
	}
	if subtle.ConstantTimeCompare(encPrivate.PublicKey().Bytes(), encPub) != 1 {
		return errors.New("X25519 public and private keys do not match")
	}
	signing, err := l.SigningPrivate()
	if err != nil {
		return err
	}
	signingPub, err := decodeExact(l.SigningPublicKey, ed25519.PublicKeySize, "Ed25519 public key")
	if err != nil {
		return err
	}
	if subtle.ConstantTimeCompare(signing.Public().(ed25519.PublicKey), signingPub) != 1 {
		return errors.New("Ed25519 public and private keys do not match")
	}
	return nil
}

func (l LocalIdentity) EncryptionPrivate() ([]byte, error) {
	return decodeExact(l.EncryptionPrivateKey, 32, "X25519 private key")
}
func (l LocalIdentity) SigningPrivate() (ed25519.PrivateKey, error) {
	b, err := decodeExact(l.SigningPrivateKey, ed25519.PrivateKeySize, "Ed25519 private key")
	return ed25519.PrivateKey(b), err
}
func (l LocalIdentity) EncryptionPrivateFor(keyID string) ([]byte, error) {
	for _, key := range l.EncryptionKeys {
		if key.KeyID == keyID {
			return decodeExact(key.PrivateKey, 32, "X25519 private key")
		}
	}
	if l.EncryptionKeyID == keyID {
		return l.EncryptionPrivate()
	}
	return nil, fmt.Errorf("local private key %s is unavailable", keyID)
}

func (l LocalIdentity) SigningPrivateFor(keyID string) (ed25519.PrivateKey, error) {
	for _, key := range l.SigningKeys {
		if key.KeyID == keyID {
			b, err := decodeExact(key.PrivateKey, ed25519.PrivateKeySize, "Ed25519 private key")
			return ed25519.PrivateKey(b), err
		}
	}
	if l.SigningKeyID == keyID {
		return l.SigningPrivate()
	}
	return nil, fmt.Errorf("local private key %s is unavailable", keyID)
}

type Recipient struct {
	Alias               string `json:"alias"`
	ServerURL           string `json:"server_url,omitempty"`
	IdentityID          string `json:"identity_id"`
	Handle              string `json:"handle,omitempty"`
	DisplayName         string `json:"display_name,omitempty"`
	EncryptionKeyID     string `json:"encryption_key_id"`
	EncryptionPublicKey string `json:"encryption_public_key"`
	SigningKeyID        string `json:"signing_key_id"`
	SigningPublicKey    string `json:"signing_public_key"`
	Fingerprint         string `json:"fingerprint"`
	TrustState          string `json:"trust_state"`
	CreatedAt           int64  `json:"created_at"`
	UpdatedAt           int64  `json:"updated_at"`
}

func (r Recipient) Validate() error {
	if r.Alias == "" || r.IdentityID == "" || r.EncryptionKeyID == "" || r.SigningKeyID == "" {
		return errors.New("invalid recipient record")
	}
	if r.ServerURL != "" {
		canonical, err := CanonicalServerURL(r.ServerURL)
		if err != nil || canonical != r.ServerURL {
			return errors.New("recipient server URL is invalid or non-canonical")
		}
	}
	fp, err := ParseFingerprint(r.Fingerprint)
	if err != nil {
		return err
	}
	if fp != r.Fingerprint {
		return errors.New("recipient fingerprint is not canonical")
	}
	enc, err := ParsePublicKey(r.EncryptionPublicKey, 32)
	if err != nil {
		return err
	}
	sign, err := ParsePublicKey(r.SigningPublicKey, ed25519.PublicKeySize)
	if err != nil {
		return err
	}
	want := Fingerprint(r.IdentityID, r.EncryptionKeyID, enc, r.SigningKeyID, sign)
	if subtle.ConstantTimeCompare([]byte(want), []byte(r.Fingerprint)) != 1 {
		return errors.New("recipient public keys do not match fingerprint")
	}
	switch r.TrustState {
	case "unverified", "pinned", "rotated", "changed", "revoked":
	default:
		return errors.New("invalid recipient trust state")
	}
	return nil
}

type recipientFile struct {
	Version    int         `json:"version"`
	Recipients []Recipient `json:"recipients"`
}

type Store struct{ root string }

func NewStore(configPath string) Store { return Store{root: filepath.Dir(configPath)} }
func (s Store) identityDir() string    { return filepath.Join(s.root, "identities") }
func (s Store) identityPath(id string) (string, error) {
	if err := safeName(id); err != nil {
		return "", err
	}
	return filepath.Join(s.identityDir(), id+".json"), nil
}

func (s Store) SaveIdentity(id LocalIdentity) (string, error) {
	if err := id.Validate(); err != nil {
		return "", err
	}
	path, err := s.identityPath(id.IdentityID)
	if err != nil {
		return "", err
	}
	if err := writePrivateJSON(path, id); err != nil {
		return "", err
	}
	return path, nil
}

func (s Store) StageIdentity(id LocalIdentity, keyID string) (string, error) {
	if err := id.Validate(); err != nil {
		return "", err
	}
	if err := safeName(id.IdentityID); err != nil {
		return "", err
	}
	if err := safeName(keyID); err != nil {
		return "", err
	}
	path := filepath.Join(s.identityDir(), ".pending-"+id.IdentityID+"-"+keyID)
	if err := writePrivateJSON(path, id); err != nil {
		return "", err
	}
	return path, nil
}
func (s Store) PromoteStagedIdentity(path, identityID string) error {
	want, err := s.identityPath(identityID)
	if err != nil {
		return err
	}
	if filepath.Dir(path) != s.identityDir() || !strings.HasPrefix(filepath.Base(path), ".pending-") {
		return errors.New("invalid staged identity path")
	}
	if err := os.Rename(path, want); err != nil {
		return err
	}
	return os.Chmod(want, 0o600)
}

func (s Store) SavePending(kind, id string, value any) (string, error) {
	path, err := s.pendingPath(kind, id)
	if err != nil {
		return "", err
	}
	return path, writePrivateJSON(path, value)
}

func (s Store) LoadPending(kind, id string, value any) error {
	path, err := s.pendingPath(kind, id)
	if err != nil {
		return err
	}
	return readPrivateJSON(path, value)
}

func (s Store) RemovePending(kind, id string) error {
	path, err := s.pendingPath(kind, id)
	if err != nil {
		return err
	}
	err = os.Remove(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func (s Store) pendingPath(kind, id string) (string, error) {
	if err := safeName(kind); err != nil {
		return "", err
	}
	if err := safeName(id); err != nil {
		return "", err
	}
	return filepath.Join(s.root, "pending", kind+"-"+id), nil
}

func (s Store) LoadIdentity(ref string) (LocalIdentity, error) {
	list, err := s.ListIdentities()
	if err != nil {
		return LocalIdentity{}, err
	}
	var match *LocalIdentity
	for i := range list {
		if list[i].IdentityID == ref || list[i].Handle == ref {
			if match != nil {
				return LocalIdentity{}, fmt.Errorf("identity reference %q is ambiguous", ref)
			}
			match = &list[i]
		}
	}
	if match == nil {
		return LocalIdentity{}, fmt.Errorf("local identity %q not found", ref)
	}
	return *match, nil
}
func (s Store) ListIdentities() ([]LocalIdentity, error) {
	entries, err := os.ReadDir(s.identityDir())
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	out := make([]LocalIdentity, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		var id LocalIdentity
		if err := readPrivateJSON(filepath.Join(s.identityDir(), e.Name()), &id); err != nil {
			return nil, err
		}
		if err := id.Validate(); err != nil {
			return nil, fmt.Errorf("%s: %w", e.Name(), err)
		}
		out = append(out, id)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Handle < out[j].Handle })
	return out, nil
}

func (s Store) recipientsPath() string { return filepath.Join(s.root, "recipients.json") }

type DeliveryContext struct {
	Version             int    `json:"version"`
	DeliveryID          string `json:"delivery_id"`
	TransferID          string `json:"transfer_id"`
	RecipientIdentityID string `json:"recipient_identity_id"`
	SigningKeyID        string `json:"signing_key_id"`
	ClaimID             string `json:"claim_id"`
	ClaimNonce          string `json:"claim_nonce"`
	ManifestSHA256      string `json:"manifest_sha256"`
	VerifiedAt          int64  `json:"verified_at"`
}

func (s Store) SaveDeliveryContext(v DeliveryContext) (string, error) {
	if v.DeliveryID == "" || v.TransferID == "" || v.RecipientIdentityID == "" || v.SigningKeyID == "" || v.ClaimID == "" || v.ClaimNonce == "" || v.ManifestSHA256 == "" {
		return "", errors.New("invalid delivery receipt context")
	}
	v.Version = localFileVersion
	if err := safeName(v.DeliveryID); err != nil {
		return "", err
	}
	path := filepath.Join(s.root, "deliveries", v.DeliveryID+".json")
	return path, writePrivateJSON(path, v)
}

func (s Store) LoadDeliveryContext(deliveryID string) (DeliveryContext, error) {
	var v DeliveryContext
	if err := safeName(deliveryID); err != nil {
		return v, err
	}
	err := readPrivateJSON(filepath.Join(s.root, "deliveries", deliveryID+".json"), &v)
	if err != nil {
		return v, err
	}
	if v.Version != localFileVersion || v.DeliveryID != deliveryID {
		return DeliveryContext{}, errors.New("invalid delivery receipt context")
	}
	return v, nil
}
func (s Store) ListRecipients() ([]Recipient, error) {
	var f recipientFile
	err := readPrivateJSON(s.recipientsPath(), &f)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if f.Version != localFileVersion {
		return nil, errors.New("unsupported recipient store version")
	}
	for _, r := range f.Recipients {
		if err := r.Validate(); err != nil {
			return nil, err
		}
	}
	sort.Slice(f.Recipients, func(i, j int) bool { return f.Recipients[i].Alias < f.Recipients[j].Alias })
	return f.Recipients, nil
}
func (s Store) PutRecipient(recipient Recipient) error {
	if err := recipient.Validate(); err != nil {
		return err
	}
	list, err := s.ListRecipients()
	if err != nil {
		return err
	}
	for i, r := range list {
		if r.Alias == recipient.Alias || r.IdentityID == recipient.IdentityID {
			list[i] = recipient
			return writePrivateJSON(s.recipientsPath(), recipientFile{Version: localFileVersion, Recipients: list})
		}
	}
	list = append(list, recipient)
	return writePrivateJSON(s.recipientsPath(), recipientFile{Version: localFileVersion, Recipients: list})
}
func (s Store) Recipient(ref string) (Recipient, error) {
	list, err := s.ListRecipients()
	if err != nil {
		return Recipient{}, err
	}
	var match *Recipient
	for i := range list {
		if list[i].Alias == ref || list[i].IdentityID == ref {
			if match != nil {
				return Recipient{}, fmt.Errorf("recipient %q is ambiguous", ref)
			}
			match = &list[i]
		}
	}
	if match == nil {
		return Recipient{}, fmt.Errorf("recipient %q not found", ref)
	}
	return *match, nil
}
func (s Store) RemoveRecipient(ref string) error {
	list, err := s.ListRecipients()
	if err != nil {
		return err
	}
	out := list[:0]
	found := false
	for _, r := range list {
		if r.Alias == ref || r.IdentityID == ref {
			found = true
			continue
		}
		out = append(out, r)
	}
	if !found {
		return fmt.Errorf("recipient %q not found", ref)
	}
	return writePrivateJSON(s.recipientsPath(), recipientFile{Version: localFileVersion, Recipients: out})
}

func ParseInvitation(value string) (serverURL, identityID, fingerprint string, err error) {
	u, e := url.Parse(strings.TrimSpace(value))
	if e != nil || u.Host == "" {
		return "", "", "", errors.New("invitation must be an absolute URL")
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && (u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1" || u.Hostname() == "::1")) {
		return "", "", "", errors.New("invitation must use HTTPS")
	}
	if u.User != nil || u.RawQuery != "" {
		return "", "", "", errors.New("invitation must not contain credentials or a query")
	}
	parts := strings.Split(strings.Trim(u.EscapedPath(), "/"), "/")
	if len(parts) != 2 || parts[0] != "i" {
		return "", "", "", errors.New("invitation path must be /i/<identity-id>")
	}
	id, e := url.PathUnescape(parts[1])
	if e != nil || id == "" || strings.Contains(id, "/") {
		return "", "", "", errors.New("invalid invitation identity ID")
	}
	if !strings.HasPrefix(u.Fragment, "fp.") {
		return "", "", "", errors.New("invitation fragment must be fp.<fingerprint>")
	}
	fp, e := ParseFingerprint(strings.TrimPrefix(u.Fragment, "fp."))
	if e != nil {
		return "", "", "", e
	}
	origin, e := CanonicalServerURL(u.Scheme + "://" + u.Host)
	if e != nil {
		return "", "", "", e
	}
	return origin, id, fp, nil
}

func CanonicalServerURL(value string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(value))
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return "", errors.New("server URL must be an origin")
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && (u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1" || u.Hostname() == "::1")) {
		return "", errors.New("server URL must use HTTPS")
	}
	return u.Scheme + "://" + u.Host, nil
}

func writePrivateJSON(path string, value any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	tmp, err := os.CreateTemp(filepath.Dir(path), ".identity-*.json")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer func() { _ = os.Remove(name) }()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(b); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(name, path); err != nil {
		return err
	}
	return os.Chmod(path, 0o600)
}
func readPrivateJSON(path string, out any) error {
	st, err := os.Stat(path)
	if err != nil {
		return err
	}
	if runtime.GOOS != "windows" && st.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("%s permissions are %04o; expected 0600", path, st.Mode().Perm())
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	dec := json.NewDecoder(strings.NewReader(string(b)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(out); err != nil {
		return err
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("private JSON file contains multiple values")
		}
		return err
	}
	return nil
}
func decodeExact(value string, size int, label string) ([]byte, error) {
	b, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil || len(b) != size || base64.RawURLEncoding.EncodeToString(b) != value {
		return nil, fmt.Errorf("invalid %s", label)
	}
	return b, nil
}
func safeName(value string) error {
	if value == "" || value == "." || value == ".." || filepath.Base(value) != value {
		return errors.New("unsafe local identity ID")
	}
	return nil
}
