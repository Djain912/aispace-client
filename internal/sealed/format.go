package sealed

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"path"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	Protocol                 = "aispace-sealed-v1"
	FragmentVersion          = "as1"
	TokenPrefix              = "ast1_"
	DefaultChunkSize         = int64(8 << 20)
	ManifestEnvelopeOverhead = 12 + 16
	MaxManifestEnvelopeBytes = 1 << 20
	MaxManifestBytes         = MaxManifestEnvelopeBytes - ManifestEnvelopeOverhead
	MaxFiles                 = 1_000
	MaxPathBytes             = 1024
)

var rawURL = base64.RawURLEncoding

// Secrets contains only recipient-side bearer material. MasterKey is never
// sent to the service; ClaimCapability is sent only in Authorization.
type Secrets struct {
	MasterKey       [32]byte
	ClaimCapability [32]byte
}

func NewSecrets() (Secrets, error) {
	var s Secrets
	if _, err := rand.Read(s.MasterKey[:]); err != nil {
		return s, fmt.Errorf("generate master key: %w", err)
	}
	if _, err := rand.Read(s.ClaimCapability[:]); err != nil {
		return s, fmt.Errorf("generate claim capability: %w", err)
	}
	return s, nil
}

func (s Secrets) Fragment() string {
	return FragmentVersion + "." + rawURL.EncodeToString(s.MasterKey[:]) + "." + rawURL.EncodeToString(s.ClaimCapability[:])
}

func (s Secrets) ClaimCapabilityString() string {
	return rawURL.EncodeToString(s.ClaimCapability[:])
}

// CLIToken returns the canonical binary-envelope token form. The transfer ID
// is authenticated by the encryption context, not treated as a secret.
func (s Secrets) CLIToken(transferID string) (string, error) {
	if transferID == "" || len(transferID) > 255 {
		return "", errors.New("invalid transfer ID length")
	}
	b := make([]byte, 2+len(transferID)+64)
	b[0] = 1
	b[1] = byte(len(transferID))
	copy(b[2:], transferID)
	copy(b[2+len(transferID):], s.MasterKey[:])
	copy(b[2+len(transferID)+32:], s.ClaimCapability[:])
	return TokenPrefix + rawURL.EncodeToString(b), nil
}

func ParseFragment(value string) (Secrets, error) {
	var out Secrets
	value = strings.TrimSpace(strings.TrimPrefix(value, "#"))
	parts := strings.Split(value, ".")
	if len(parts) != 3 || parts[0] != FragmentVersion {
		return out, errors.New("transfer fragment must be as1.<master-key>.<claim-capability>")
	}
	key, err := rawURL.DecodeString(parts[1])
	if err != nil || len(key) != 32 || rawURL.EncodeToString(key) != parts[1] {
		return out, errors.New("transfer fragment has an invalid master key")
	}
	claim, err := rawURL.DecodeString(parts[2])
	if err != nil || len(claim) != 32 || rawURL.EncodeToString(claim) != parts[2] {
		return out, errors.New("transfer fragment has an invalid claim capability")
	}
	copy(out.MasterKey[:], key)
	copy(out.ClaimCapability[:], claim)
	return out, nil
}

// ParseReference accepts an HTTPS/local-development transfer URL, a bare
// fragment, or the canonical CLI token. It never returns the original secret.
func ParseReference(value string) (transferID string, secrets Secrets, err error) {
	value = strings.TrimSpace(value)
	if strings.HasPrefix(value, TokenPrefix) {
		return parseCLIToken(value)
	}
	if strings.HasPrefix(value, FragmentVersion+".") || strings.HasPrefix(value, "#"+FragmentVersion+".") {
		secrets, err = ParseFragment(value)
		return "", secrets, err
	}
	u, parseErr := url.Parse(value)
	if parseErr != nil || u.Host == "" {
		return "", secrets, errors.New("transfer reference must be a link or canonical CLI token")
	}
	if u.User != nil || u.RawQuery != "" {
		return "", secrets, errors.New("transfer link must not contain credentials or a query string")
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && isLoopbackHost(u.Hostname())) {
		return "", secrets, errors.New("transfer link must use HTTPS")
	}
	clean := path.Clean(u.EscapedPath())
	if clean != u.EscapedPath() {
		return "", secrets, errors.New("transfer link path is not canonical")
	}
	parts := strings.Split(strings.Trim(clean, "/"), "/")
	if len(parts) != 2 || parts[0] != "t" {
		return "", secrets, errors.New("transfer link path must be /t/<transfer-id>")
	}
	id, unescapeErr := url.PathUnescape(parts[1])
	if unescapeErr != nil || id == "" {
		return "", secrets, errors.New("transfer link has an invalid transfer ID")
	}
	secrets, err = ParseFragment(u.Fragment)
	return id, secrets, err
}

func parseCLIToken(value string) (string, Secrets, error) {
	var s Secrets
	b, err := rawURL.DecodeString(strings.TrimPrefix(value, TokenPrefix))
	encoded := strings.TrimPrefix(value, TokenPrefix)
	if err != nil || len(b) < 2 || rawURL.EncodeToString(b) != encoded {
		return "", s, errors.New("invalid sealed transfer token")
	}
	if b[0] != 1 {
		return "", s, errors.New("unsupported sealed transfer token version")
	}
	n := int(b[1])
	if n == 0 || len(b) != 2+n+64 {
		return "", s, errors.New("invalid sealed transfer token length")
	}
	id := string(b[2 : 2+n])
	if !utf8.ValidString(id) {
		return "", s, errors.New("invalid transfer ID encoding")
	}
	copy(s.MasterKey[:], b[2+n:2+n+32])
	copy(s.ClaimCapability[:], b[2+n+32:])
	return id, s, nil
}

func isLoopbackHost(host string) bool {
	return host == "localhost" || strings.HasSuffix(host, ".localhost") || host == "127.0.0.1" || host == "::1"
}

// Manifest is encrypted in full. CiphertextSHA256 covers only the
// concatenated encrypted data chunks, excluding this manifest envelope.
type Manifest struct {
	Protocol         string             `json:"protocol"`
	TransferID       string             `json:"transfer_id"`
	CreatedAt        int64              `json:"created_at"`
	ExpiresAt        int64              `json:"expires_at"`
	CiphertextSHA256 string             `json:"ciphertext_sha256"`
	Files            []ManifestFile     `json:"files"`
	Sender           *ManifestSender    `json:"sender"`
	Recipient        *ManifestRecipient `json:"recipient,omitempty"`
	Delivery         *ManifestDelivery  `json:"delivery,omitempty"`
	Signature        *ManifestSignature `json:"signature"`
}

type ManifestSender struct {
	IdentityID   string `json:"identity_id"`
	SigningKeyID string `json:"signing_key_id"`
}
type ManifestRecipient struct {
	IdentityID      string `json:"identity_id"`
	EncryptionKeyID string `json:"encryption_key_id"`
}
type ManifestDelivery struct {
	Mode                   string  `json:"mode"`
	AlsoLink               bool    `json:"also_link"`
	MaxDownloads           *int64  `json:"max_downloads"`
	RecipientIdentityID    string  `json:"recipient_identity_id"`
	RecipientKeyID         string  `json:"recipient_key_id"`
	SenderIdentityID       *string `json:"sender_identity_id"`
	SenderSigningKeyID     *string `json:"sender_signing_key_id"`
	CreatedAt              int64   `json:"created_at"`
	ExpiresAt              int64   `json:"expires_at"`
	FileCount              int     `json:"file_count"`
	DeclaredPlaintextBytes int64   `json:"declared_plaintext_bytes"`
}
type ManifestSignature struct {
	Algorithm string `json:"algorithm"`
	Value     string `json:"value"`
}

type ManifestFile struct {
	Index       int             `json:"index"`
	Path        string          `json:"path"`
	ContentType string          `json:"content_type"`
	SizeBytes   int64           `json:"size_bytes"`
	SHA256      string          `json:"sha256"`
	Chunks      []ManifestChunk `json:"chunks"`
}

type ManifestChunk struct {
	GlobalIndex    uint64 `json:"global_index"`
	PlaintextBytes int64  `json:"plaintext_bytes"`
}

// CanonicalManifest serializes the fixed v1 schema deterministically. The
// schema has no map-valued fields; field order is therefore part of this
// version's canonical representation.
func CanonicalManifest(m Manifest) ([]byte, error) {
	if err := ValidateManifest(m); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(m)
	if err != nil {
		return nil, err
	}
	var value any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&value); err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(value); err != nil {
		return nil, err
	}
	b := bytes.TrimSuffix(buf.Bytes(), []byte("\n"))
	b = bytes.ReplaceAll(b, []byte(`\u2028`), []byte("\u2028"))
	b = bytes.ReplaceAll(b, []byte(`\u2029`), []byte("\u2029"))
	if len(b) > MaxManifestBytes {
		return nil, errors.New("sealed manifest exceeds size limit")
	}
	return b, nil
}

// CanonicalUnsignedManifest returns the RFC 8785 signing payload with the
// signature member omitted entirely.
func CanonicalUnsignedManifest(m Manifest) ([]byte, error) {
	validation := m
	if validation.Sender != nil && validation.Signature == nil {
		validation.Signature = &ManifestSignature{Algorithm: "Ed25519", Value: "unsigned"}
	}
	if err := ValidateManifest(validation); err != nil {
		return nil, err
	}
	type unsigned struct {
		Protocol         string             `json:"protocol"`
		TransferID       string             `json:"transfer_id"`
		CreatedAt        int64              `json:"created_at"`
		ExpiresAt        int64              `json:"expires_at"`
		CiphertextSHA256 string             `json:"ciphertext_sha256"`
		Files            []ManifestFile     `json:"files"`
		Sender           *ManifestSender    `json:"sender"`
		Recipient        *ManifestRecipient `json:"recipient,omitempty"`
		Delivery         *ManifestDelivery  `json:"delivery,omitempty"`
	}
	return canonicalRFC8785(unsigned{m.Protocol, m.TransferID, m.CreatedAt, m.ExpiresAt, m.CiphertextSHA256, m.Files, m.Sender, m.Recipient, m.Delivery})
}

func DecodeManifest(b []byte) (Manifest, error) {
	var m Manifest
	if len(b) == 0 || len(b) > MaxManifestBytes {
		return m, errors.New("sealed manifest size is invalid")
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&m); err != nil {
		return m, fmt.Errorf("decode sealed manifest: %w", err)
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return m, errors.New("sealed manifest has trailing data")
	}
	if err := ValidateManifest(m); err != nil {
		return m, err
	}
	canonical, err := CanonicalManifest(m)
	if err != nil {
		return m, err
	}
	if !bytes.Equal(b, canonical) {
		return m, errors.New("sealed manifest is not in canonical JSON form")
	}
	return m, nil
}

func ValidateManifest(m Manifest) error {
	if m.Protocol != Protocol || m.TransferID == "" {
		return errors.New("sealed manifest protocol or transfer ID is invalid")
	}
	if m.CreatedAt <= 0 || m.ExpiresAt <= m.CreatedAt {
		return errors.New("sealed manifest timestamps are invalid")
	}
	if !isHexDigest(m.CiphertextSHA256) {
		return errors.New("sealed manifest ciphertext digest is invalid")
	}
	if len(m.Files) == 0 || len(m.Files) > MaxFiles {
		return errors.New("sealed manifest file count is invalid")
	}
	if (m.Sender == nil) != (m.Signature == nil) {
		return errors.New("sealed manifest sender and signature must appear together")
	}
	if m.Sender != nil && (m.Sender.IdentityID == "" || m.Sender.SigningKeyID == "" || m.Signature.Algorithm != "Ed25519" || m.Signature.Value == "") {
		return errors.New("sealed manifest sender signature is invalid")
	}
	if m.Recipient != nil && (m.Recipient.IdentityID == "" || m.Recipient.EncryptionKeyID == "") {
		return errors.New("sealed manifest recipient binding is invalid")
	}
	if (m.Recipient == nil) != (m.Delivery == nil) {
		return errors.New("sealed manifest addressed recipient and delivery policy must appear together")
	}
	seenPaths := make(map[string]struct{}, len(m.Files))
	var wantGlobal uint64
	for i, f := range m.Files {
		if f.Index != i || f.SizeBytes < 0 || !isHexDigest(f.SHA256) {
			return fmt.Errorf("sealed manifest file %d metadata is invalid", i)
		}
		if err := validatePath(f.Path); err != nil {
			return fmt.Errorf("sealed manifest file %d: %w", i, err)
		}
		if _, exists := seenPaths[f.Path]; exists {
			return fmt.Errorf("sealed manifest repeats path %q", f.Path)
		}
		seenPaths[f.Path] = struct{}{}
		if len(f.Chunks) == 0 {
			return fmt.Errorf("sealed manifest file %d has no chunks", i)
		}
		var fileBytes int64
		for _, ch := range f.Chunks {
			if ch.GlobalIndex != wantGlobal || ch.PlaintextBytes < 0 || ch.PlaintextBytes > DefaultChunkSize {
				return fmt.Errorf("sealed manifest chunk %d is invalid", ch.GlobalIndex)
			}
			fileBytes += ch.PlaintextBytes
			wantGlobal++
		}
		if fileBytes != f.SizeBytes {
			return fmt.Errorf("sealed manifest file %d chunk sizes do not match file size", i)
		}
	}
	if m.Delivery != nil {
		d := m.Delivery
		if d.Mode != "addressed" || d.RecipientIdentityID != m.Recipient.IdentityID || d.RecipientKeyID != m.Recipient.EncryptionKeyID || d.CreatedAt != m.CreatedAt || d.ExpiresAt != m.ExpiresAt || d.FileCount != len(m.Files) || d.DeclaredPlaintextBytes < 0 {
			return errors.New("sealed manifest addressed delivery policy is invalid")
		}
		if d.MaxDownloads != nil && *d.MaxDownloads <= 0 {
			return errors.New("sealed manifest maximum downloads is invalid")
		}
		if (m.Sender == nil) != (d.SenderIdentityID == nil) || (m.Sender == nil) != (d.SenderSigningKeyID == nil) {
			return errors.New("sealed manifest sender delivery binding is invalid")
		}
		if m.Sender != nil && (*d.SenderIdentityID != m.Sender.IdentityID || *d.SenderSigningKeyID != m.Sender.SigningKeyID) {
			return errors.New("sealed manifest sender delivery binding does not match sender")
		}
		var total int64
		for _, f := range m.Files {
			total += f.SizeBytes
		}
		if total != d.DeclaredPlaintextBytes {
			return errors.New("sealed manifest declared plaintext bytes do not match files")
		}
	}
	return nil
}

func validatePath(p string) error {
	if p == "" || len(p) > MaxPathBytes || !utf8.ValidString(p) || strings.HasPrefix(p, "/") || strings.Contains(p, "\\") {
		return errors.New("path is not a bounded relative UTF-8 slash path")
	}
	for _, r := range p {
		if r < 0x20 || r == 0x7f {
			return errors.New("path contains a control character")
		}
	}
	for _, segment := range strings.Split(p, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return errors.New("path contains an unsafe segment")
		}
	}
	return nil
}

func isHexDigest(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, r := range s {
		if !strings.ContainsRune("0123456789abcdef", r) {
			return false
		}
	}
	return true
}

func dataAAD(transferID string, objectIndex int, globalIndex uint64, fileChunkIndex int, plaintextLength int64) []byte {
	return []byte(strings.Join([]string{
		Protocol,
		transferID,
		strconv.Itoa(objectIndex),
		strconv.FormatUint(globalIndex, 10),
		strconv.Itoa(fileChunkIndex),
		strconv.FormatInt(plaintextLength, 10),
	}, "\n"))
}

func putUint64BE(dst []byte, value uint64) { binary.BigEndian.PutUint64(dst, value) }
