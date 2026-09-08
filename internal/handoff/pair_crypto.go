package handoff

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
)

const pairEnvelopeVersion byte = 1

type ReceiverKey struct{ private *ecdh.PrivateKey }

func NewReceiverKey() (*ReceiverKey, string, error) {
	private, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		return nil, "", fmt.Errorf("generate handoff key: %w", err)
	}
	return &ReceiverKey{private: private}, rawURL.EncodeToString(private.PublicKey().Bytes()), nil
}

// SealForReceiver encrypts a canonical intent token to the receiver's one-use
// ephemeral key. The pairing service sees only this ciphertext envelope.
func SealForReceiver(receiverPublicKey, attemptID string, plaintext []byte) (string, error) {
	publicBytes, err := rawURL.DecodeString(receiverPublicKey)
	if err != nil || rawURL.EncodeToString(publicBytes) != receiverPublicKey {
		return "", errors.New("receiver handoff key is malformed")
	}
	public, err := ecdh.P256().NewPublicKey(publicBytes)
	if err != nil {
		return "", errors.New("receiver handoff key is invalid")
	}
	private, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		return "", err
	}
	shared, err := private.ECDH(public)
	if err != nil {
		return "", errors.New("derive handoff secret")
	}
	aead, err := pairingAEAD(shared, attemptID)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	aad := []byte("aispace-handoff-v1\x00" + attemptID)
	ciphertext := aead.Seal(nil, nonce, plaintext, aad)
	envelope := append([]byte{pairEnvelopeVersion}, private.PublicKey().Bytes()...)
	envelope = append(envelope, nonce...)
	envelope = append(envelope, ciphertext...)
	return base64.RawURLEncoding.EncodeToString(envelope), nil
}

func (r *ReceiverKey) Open(attemptID, encoded string) ([]byte, error) {
	if r == nil || r.private == nil {
		return nil, errors.New("receiver handoff key is unavailable")
	}
	envelope, err := base64.RawURLEncoding.DecodeString(encoded)
	const publicSize = 65
	if err != nil || base64.RawURLEncoding.EncodeToString(envelope) != encoded || len(envelope) < 1+publicSize+12+16 || envelope[0] != pairEnvelopeVersion {
		return nil, errors.New("handoff envelope is malformed")
	}
	public, err := ecdh.P256().NewPublicKey(envelope[1 : 1+publicSize])
	if err != nil {
		return nil, errors.New("handoff envelope key is invalid")
	}
	shared, err := r.private.ECDH(public)
	if err != nil {
		return nil, errors.New("derive handoff secret")
	}
	aead, err := pairingAEAD(shared, attemptID)
	if err != nil {
		return nil, err
	}
	nonceStart := 1 + publicSize
	nonceEnd := nonceStart + aead.NonceSize()
	return aead.Open(nil, envelope[nonceStart:nonceEnd], envelope[nonceEnd:], []byte("aispace-handoff-v1\x00"+attemptID))
}

func pairingAEAD(shared []byte, attemptID string) (cipher.AEAD, error) {
	key, err := hkdf.Key(sha256.New, shared, []byte(attemptID), "aispace-handoff-v1", 32)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}
