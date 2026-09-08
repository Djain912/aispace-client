package sealed

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"

	"golang.org/x/crypto/hkdf"
)

const gcmTagSize = 16

type keys struct {
	manifest    [32]byte
	data        [32]byte
	noncePrefix [4]byte
}

func deriveKeys(master [32]byte, transferID string) (keys, error) {
	var out keys
	salt := []byte(Protocol + "\x00" + transferID)
	for _, d := range []struct {
		info string
		dst  []byte
	}{
		{"manifest-key", out.manifest[:]},
		{"data-key", out.data[:]},
		{"nonce-prefix", out.noncePrefix[:]},
	} {
		if _, err := io.ReadFull(hkdf.New(sha256.New, master[:], salt, []byte(d.info)), d.dst); err != nil {
			return keys{}, fmt.Errorf("derive %s: %w", d.info, err)
		}
	}
	return out, nil
}

func newGCM(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// EncryptChunk returns plaintext plus the 16-byte AES-GCM authentication tag.
func EncryptChunk(master [32]byte, transferID string, objectIndex int, globalIndex uint64, fileChunkIndex int, plaintext []byte) ([]byte, error) {
	k, err := deriveKeys(master, transferID)
	if err != nil {
		return nil, err
	}
	gcm, err := newGCM(k.data[:])
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	copy(nonce, k.noncePrefix[:])
	putUint64BE(nonce[4:], globalIndex)
	return gcm.Seal(nil, nonce, plaintext, dataAAD(transferID, objectIndex, globalIndex, fileChunkIndex, int64(len(plaintext)))), nil
}

func DecryptChunk(master [32]byte, transferID string, objectIndex int, globalIndex uint64, fileChunkIndex int, plaintextLength int64, ciphertext []byte) ([]byte, error) {
	if plaintextLength < 0 || plaintextLength > DefaultChunkSize || int64(len(ciphertext)) != plaintextLength+gcmTagSize {
		return nil, errors.New("sealed chunk length is invalid")
	}
	k, err := deriveKeys(master, transferID)
	if err != nil {
		return nil, err
	}
	gcm, err := newGCM(k.data[:])
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	copy(nonce, k.noncePrefix[:])
	putUint64BE(nonce[4:], globalIndex)
	plain, err := gcm.Open(nil, nonce, ciphertext, dataAAD(transferID, objectIndex, globalIndex, fileChunkIndex, plaintextLength))
	if err != nil {
		return nil, errors.New("sealed chunk authentication failed")
	}
	return plain, nil
}

// EncryptManifest emits random 12-byte nonce || ciphertext || GCM tag.
func EncryptManifest(master [32]byte, transferID string, canonical []byte) ([]byte, error) {
	if len(canonical) == 0 || len(canonical) > MaxManifestBytes {
		return nil, errors.New("sealed manifest size is invalid")
	}
	k, err := deriveKeys(master, transferID)
	if err != nil {
		return nil, err
	}
	gcm, err := newGCM(k.manifest[:])
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("generate manifest nonce: %w", err)
	}
	aad := []byte(Protocol + "\n" + transferID + "\nmanifest")
	envelope := append(nonce, gcm.Seal(nil, nonce, canonical, aad)...)
	if len(envelope) > MaxManifestEnvelopeBytes {
		return nil, errors.New("sealed manifest envelope exceeds size limit")
	}
	return envelope, nil
}

func DecryptManifest(master [32]byte, transferID string, envelope []byte) ([]byte, error) {
	if len(envelope) > MaxManifestEnvelopeBytes {
		return nil, errors.New("sealed manifest envelope exceeds size limit")
	}
	k, err := deriveKeys(master, transferID)
	if err != nil {
		return nil, err
	}
	gcm, err := newGCM(k.manifest[:])
	if err != nil {
		return nil, err
	}
	if len(envelope) < gcm.NonceSize()+gcm.Overhead() {
		return nil, errors.New("sealed manifest envelope is truncated")
	}
	nonce := envelope[:gcm.NonceSize()]
	aad := []byte(Protocol + "\n" + transferID + "\nmanifest")
	plain, err := gcm.Open(nil, nonce, envelope[gcm.NonceSize():], aad)
	if err != nil {
		return nil, errors.New("sealed manifest authentication failed")
	}
	return plain, nil
}

func SHA256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
