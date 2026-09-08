package handoff

import (
	"bytes"
	"crypto/ecdh"
	"encoding/base64"
	"testing"
)

func TestPairEnvelopeRoundTripAndBinding(t *testing.T) {
	receiver, public, err := NewReceiverKey()
	if err != nil {
		t.Fatal(err)
	}
	want := []byte("aispace-transfer-v1.secret")
	envelope, err := SealForReceiver(public, "01ATTEMPT", want)
	if err != nil {
		t.Fatal(err)
	}
	got, err := receiver.Open("01ATTEMPT", envelope)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("open = %q, %v", got, err)
	}
	if _, err := receiver.Open("01OTHER", envelope); err == nil {
		t.Fatal("accepted envelope for another attempt")
	}
}

func TestGoWebCryptoPairingVector(t *testing.T) {
	receiverScalar := make([]byte, 32)
	receiverScalar[31] = 1
	senderScalar := make([]byte, 32)
	senderScalar[31] = 2
	receiver, err := ecdh.P256().NewPrivateKey(receiverScalar)
	if err != nil {
		t.Fatal(err)
	}
	sender, err := ecdh.P256().NewPrivateKey(senderScalar)
	if err != nil {
		t.Fatal(err)
	}
	shared, err := sender.ECDH(receiver.PublicKey())
	if err != nil {
		t.Fatal(err)
	}
	const attemptID = "01ARZ3NDEKTSV4RRFFQ69G5FAV"
	const plaintext = "go-webcrypto-pairing-vector"
	aead, err := pairingAEAD(shared, attemptID)
	if err != nil {
		t.Fatal(err)
	}
	nonce := []byte{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11}
	ciphertext := aead.Seal(nil, nonce, []byte(plaintext), []byte("aispace-handoff-v1\x00"+attemptID))
	envelope := append([]byte{pairEnvelopeVersion}, sender.PublicKey().Bytes()...)
	envelope = append(envelope, nonce...)
	envelope = append(envelope, ciphertext...)
	const wantEnvelope = "AQR88nsYjQNPfopSOAMEtRrDwIlp4nfyGzWmC0j8R2aZeAd3VRDbjtBAKT2axp90MNu6fa3mPOmCKZ4Et50ieHPRAAECAwQFBgcICQoLLq_oPO_fzv9iOKQgOTBxcqZS-aI4nr7I7TwqKoti6UFCMtA-Bs4XPOeWlQ"
	if got := base64.RawURLEncoding.EncodeToString(envelope); got != wantEnvelope {
		t.Fatalf("Go/WebCrypto vector changed:\n got %s\nwant %s", got, wantEnvelope)
	}
}
