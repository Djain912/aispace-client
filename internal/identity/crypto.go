// Package identity implements the local cryptographic and trust state used by
// aispace agent identities. Private keys never cross this package's API as
// JSON wire values.
package identity

import (
	"bytes"
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"filippo.io/hpke"
)

const (
	Protocol                   = "aispace-identity-v1"
	KeyEnvelopeProtocol        = "aispace-key-envelope-v1"
	PossessionDomain           = "aispace-identity-possession-v1\x00"
	ManifestSignatureDomain    = "aispace-signed-manifest-v1\x00"
	ReceiptSignatureDomain     = "aispace-delivery-receipt-v1\x00"
	SuccessorSignatureDomain   = "aispace-identity-successor-v1\x00"
	EncryptionAlgorithm        = "X25519"
	SigningAlgorithm           = "Ed25519"
	FingerprintEncodedByteSize = 32
)

var rawURL = base64.RawURLEncoding

type KeyPair struct {
	EncryptionPrivate []byte
	EncryptionPublic  []byte
	SigningPrivate    ed25519.PrivateKey
	SigningPublic     ed25519.PublicKey
}

func GenerateKeyPair() (KeyPair, error) {
	enc, err := hpke.DHKEM(ecdh.X25519()).GenerateKey()
	if err != nil {
		return KeyPair{}, fmt.Errorf("generate X25519 key: %w", err)
	}
	encPrivate, err := enc.Bytes()
	if err != nil {
		return KeyPair{}, fmt.Errorf("serialize X25519 key: %w", err)
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return KeyPair{}, fmt.Errorf("generate Ed25519 key: %w", err)
	}
	return KeyPair{
		EncryptionPrivate: append([]byte(nil), encPrivate...),
		EncryptionPublic:  append([]byte(nil), enc.PublicKey().Bytes()...),
		SigningPrivate:    append(ed25519.PrivateKey(nil), priv...),
		SigningPublic:     append(ed25519.PublicKey(nil), pub...),
	}, nil
}

func PublicKeyString(key []byte) string { return rawURL.EncodeToString(key) }

func ParsePublicKey(value string, size int) ([]byte, error) {
	b, err := rawURL.DecodeString(value)
	if err != nil || len(b) != size || rawURL.EncodeToString(b) != value {
		return nil, errors.New("invalid canonical base64url public key")
	}
	return b, nil
}

// Fingerprint covers the complete active public-key binding, not one loose
// key. The NUL separators make the tuple unambiguous.
func Fingerprint(identityID, encryptionKeyID string, encryptionPublic []byte, signingKeyID string, signingPublic []byte) string {
	h := sha256.New()
	for _, field := range [][]byte{
		[]byte(Protocol), []byte(identityID), []byte(encryptionKeyID), encryptionPublic,
		[]byte(signingKeyID), signingPublic,
	} {
		h.Write(field)
		h.Write([]byte{0})
	}
	return strings.ToUpper(hex.EncodeToString(h.Sum(nil)))
}

func FormatFingerprint(value string) string {
	value = strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(value), " ", ""))
	var groups []string
	for len(value) > 0 {
		n := min(4, len(value))
		groups = append(groups, value[:n])
		value = value[n:]
	}
	return strings.Join(groups, " ")
}

func ParseFingerprint(value string) (string, error) {
	compact := strings.ToUpper(strings.Join(strings.Fields(value), ""))
	if len(compact) != FingerprintEncodedByteSize*2 {
		return "", errors.New("fingerprint must contain exactly 32 bytes")
	}
	b, err := hex.DecodeString(compact)
	if err != nil || len(b) != FingerprintEncodedByteSize {
		return "", errors.New("fingerprint must contain only hexadecimal digits")
	}
	return compact, nil
}

func HPKEInfo(transferID, recipientIdentityID, recipientKeyID string) []byte {
	return []byte(strings.Join([]string{KeyEnvelopeProtocol, transferID, recipientIdentityID, recipientKeyID}, "\x00"))
}

func WrapMasterKey(recipientPublic []byte, transferID, recipientIdentityID, recipientKeyID string, masterKey [32]byte) ([]byte, error) {
	if len(recipientPublic) != 32 || transferID == "" || recipientIdentityID == "" || recipientKeyID == "" {
		return nil, errors.New("invalid HPKE recipient context")
	}
	pub, err := hpke.DHKEM(ecdh.X25519()).NewPublicKey(recipientPublic)
	if err != nil {
		return nil, fmt.Errorf("parse X25519 public key: %w", err)
	}
	enc, sender, err := hpke.NewSender(pub, hpke.HKDFSHA256(), hpke.AES256GCM(), HPKEInfo(transferID, recipientIdentityID, recipientKeyID))
	if err != nil {
		return nil, err
	}
	ct, err := sender.Seal(nil, masterKey[:])
	if err != nil {
		return nil, err
	}
	envelope := make([]byte, 0, len(enc)+len(ct))
	envelope = append(envelope, enc...)
	envelope = append(envelope, ct...)
	return envelope, nil
}

func UnwrapMasterKey(recipientPrivate []byte, transferID, recipientIdentityID, recipientKeyID string, envelope []byte) ([32]byte, error) {
	var master [32]byte
	if len(recipientPrivate) != 32 || len(envelope) == 0 || transferID == "" || recipientIdentityID == "" || recipientKeyID == "" {
		return master, errors.New("invalid HPKE recipient context")
	}
	priv, err := hpke.DHKEM(ecdh.X25519()).NewPrivateKey(recipientPrivate)
	if err != nil {
		return master, fmt.Errorf("parse X25519 private key: %w", err)
	}
	if len(envelope) < 32 {
		return master, errors.New("HPKE envelope is truncated")
	}
	// Cap the encapsulated-key slice at its length. hpke's X25519 KEM appends
	// its context to this slice; allowing spare capacity would overwrite the
	// ciphertext that follows it in our compact envelope.
	enc := envelope[:32:32]
	recipient, err := hpke.NewRecipient(enc, priv, hpke.HKDFSHA256(), hpke.AES256GCM(), HPKEInfo(transferID, recipientIdentityID, recipientKeyID))
	if err != nil {
		return master, errors.New("HPKE envelope authentication failed")
	}
	plain, err := recipient.Open(nil, envelope[32:])
	if err != nil {
		return master, errors.New("HPKE envelope authentication failed")
	}
	if len(plain) != len(master) {
		return master, errors.New("HPKE envelope contains an invalid master key")
	}
	copy(master[:], plain)
	return master, nil
}

func SignCanonical(private ed25519.PrivateKey, domain string, canonical []byte) ([]byte, error) {
	if len(private) != ed25519.PrivateKeySize || domain == "" || !bytes.HasSuffix([]byte(domain), []byte{0}) {
		return nil, errors.New("invalid signing key or domain")
	}
	message := canonical
	if domain == ManifestSignatureDomain {
		digest := sha256.Sum256(append([]byte(domain), canonical...))
		message = digest[:]
	} else {
		message = append([]byte(domain), canonical...)
	}
	return ed25519.Sign(private, message), nil
}

func VerifyCanonical(public ed25519.PublicKey, domain string, canonical, signature []byte) bool {
	if len(public) != ed25519.PublicKeySize || len(signature) != ed25519.SignatureSize || domain == "" {
		return false
	}
	message := canonical
	if domain == ManifestSignatureDomain {
		digest := sha256.Sum256(append([]byte(domain), canonical...))
		message = digest[:]
	} else {
		message = append([]byte(domain), canonical...)
	}
	return ed25519.Verify(public, message, signature)
}
