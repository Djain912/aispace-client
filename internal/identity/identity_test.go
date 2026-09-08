package identity

import (
	"bytes"
	"crypto/ecdh"
	"crypto/ed25519"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"filippo.io/hpke"
)

func TestHPKERoundTripAndContextTamper(t *testing.T) {
	keys, err := GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	var master [32]byte
	for i := range master {
		master[i] = byte(i)
	}
	envelope, err := WrapMasterKey(keys.EncryptionPublic, "tr_1", "id_1", "key_1", master)
	if err != nil {
		t.Fatal(err)
	}
	got, err := UnwrapMasterKey(keys.EncryptionPrivate, "tr_1", "id_1", "key_1", envelope)
	if err != nil {
		t.Fatal(err)
	}
	if got != master {
		t.Fatal("unwrapped master key differs")
	}
	for _, tc := range []struct{ name, transfer, identity, key string }{{"transfer", "tr_2", "id_1", "key_1"}, {"identity", "tr_1", "id_2", "key_1"}, {"key", "tr_1", "id_1", "key_2"}} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := UnwrapMasterKey(keys.EncryptionPrivate, tc.transfer, tc.identity, tc.key, envelope); err == nil {
				t.Fatal("context tamper authenticated")
			}
		})
	}
	tampered := append([]byte(nil), envelope...)
	tampered[len(tampered)-1] ^= 1
	if _, err := UnwrapMasterKey(keys.EncryptionPrivate, "tr_1", "id_1", "key_1", tampered); err == nil {
		t.Fatal("ciphertext tamper authenticated")
	}
}

func TestCanonicalJSONRFC8785Subset(t *testing.T) {
	got, err := CanonicalJSON(map[string]any{"z": "€", "a": []any{int64(2), "\u2028", "<&>"}, "😀": true})
	if err != nil {
		t.Fatal(err)
	}
	want := "{\"a\":[2,\"\u2028\",\"<&>\"],\"z\":\"€\",\"😀\":true}"
	if string(got) != want {
		t.Fatalf("got %s", got)
	}
	if _, err := CanonicalJSON(map[string]any{"bad": 1.5}); err == nil {
		t.Fatal("float accepted")
	}
}

func TestSignDomainsAndTamper(t *testing.T) {
	keys, err := GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	canonical := []byte(`{"a":1}`)
	sig, err := SignCanonical(keys.SigningPrivate, ReceiptSignatureDomain, canonical)
	if err != nil {
		t.Fatal(err)
	}
	if !VerifyCanonical(keys.SigningPublic, ReceiptSignatureDomain, canonical, sig) {
		t.Fatal("valid signature rejected")
	}
	if VerifyCanonical(keys.SigningPublic, PossessionDomain, canonical, sig) {
		t.Fatal("cross-domain signature accepted")
	}
	canonical[5] = '2'
	if VerifyCanonical(keys.SigningPublic, ReceiptSignatureDomain, canonical, sig) {
		t.Fatal("tamper accepted")
	}
}

func TestFingerprintAndInvitationStrict(t *testing.T) {
	keys, err := GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	fp := Fingerprint("identity", "enc", keys.EncryptionPublic, "sig", keys.SigningPublic)
	if len(fp) != 64 {
		t.Fatal(fp)
	}
	formatted := FormatFingerprint(fp)
	if got, err := ParseFingerprint(formatted); err != nil || got != fp {
		t.Fatalf("%q %v", got, err)
	}
	server, id, got, err := ParseInvitation("https://aispace.sh/i/identity#fp." + fp)
	if err != nil || server != "https://aispace.sh" || id != "identity" || got != fp {
		t.Fatalf("%s %s %s %v", server, id, got, err)
	}
	for _, bad := range []string{"http://aispace.sh/i/identity#fp." + fp, "https://aispace.sh/x/identity#fp." + fp, "https://aispace.sh/i/identity#fp.1234", "https://aispace.sh/i/identity?q=1#fp." + fp} {
		if _, _, _, err := ParseInvitation(bad); err == nil {
			t.Fatalf("accepted %s", bad)
		}
	}
}

func TestStoreRequiresPrivatePermissions(t *testing.T) {
	if os.Getenv("GOOS") == "windows" {
		t.Skip()
	}
	dir := t.TempDir()
	store := NewStore(filepath.Join(dir, "config.json"))
	keys, err := GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	local := NewLocalIdentity("https://aispace.sh", "id", "handle", "Name", "enc", "sig", 1, keys)
	path, err := store.SaveIdentity(local)
	if err != nil {
		t.Fatal(err)
	}
	if st, _ := os.Stat(path); st.Mode().Perm() != 0o600 {
		t.Fatalf("mode %o", st.Mode().Perm())
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := store.LoadIdentity("id"); err == nil || !strings.Contains(err.Error(), "expected 0600") {
		t.Fatalf("got %v", err)
	}
}

func TestPendingReplayStateRequiresPrivatePermissions(t *testing.T) {
	if os.Getenv("GOOS") == "windows" {
		t.Skip()
	}
	store := NewStore(filepath.Join(t.TempDir(), "config.json"))
	path, err := store.SavePending("rotation", "identity", map[string]string{"idempotency_key": "secret-replay-key"})
	if err != nil {
		t.Fatal(err)
	}
	if st, err := os.Stat(path); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("pending mode/stat = %v, %v", st, err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	var value map[string]string
	if err := store.LoadPending("rotation", "identity", &value); err == nil || !strings.Contains(err.Error(), "expected 0600") {
		t.Fatalf("insecure pending replay state accepted: %v", err)
	}
}

func TestStoreRejectsTrailingPrivateDataAndUnsafeStagePath(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(filepath.Join(dir, "config.json"))
	keys, err := GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	local := NewLocalIdentity("https://aispace.sh", "id", "handle", "Name", "enc", "sig", 1, keys)
	path, err := store.SaveIdentity(local)
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("{}\n"); err != nil {
		_ = f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := store.LoadIdentity("id"); err == nil {
		t.Fatal("accepted trailing private JSON data")
	}

	local.IdentityID = "../escape"
	if _, err := store.StageIdentity(local, "next"); err == nil {
		t.Fatal("accepted unsafe staged identity path")
	}
}

func TestRecipientChangedKeyDoesNotSilentlyReplacePin(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(filepath.Join(dir, "config.json"))
	a, _ := GenerateKeyPair()
	b, _ := GenerateKeyPair()
	mk := func(alias string, k KeyPair) Recipient {
		fp := Fingerprint("id", "enc", k.EncryptionPublic, "sig", k.SigningPublic)
		return Recipient{Alias: alias, IdentityID: "id", EncryptionKeyID: "enc", EncryptionPublicKey: rawURL.EncodeToString(k.EncryptionPublic), SigningKeyID: "sig", SigningPublicKey: rawURL.EncodeToString(k.SigningPublic), Fingerprint: fp, TrustState: "pinned", CreatedAt: 1, UpdatedAt: 1}
	}
	if err := store.PutRecipient(mk("bot", a)); err != nil {
		t.Fatal(err)
	}
	changed := mk("bot", b)
	if bytes.Equal(a.SigningPublic, b.SigningPublic) {
		t.Fatal("unexpected duplicate")
	}
	old, _ := store.Recipient("bot")
	if old.Fingerprint == changed.Fingerprint {
		t.Fatal("test keys match")
	}
	changed.TrustState = "changed"
	if err := store.PutRecipient(changed); err != nil {
		t.Fatal(err)
	}
	got, _ := store.Recipient("bot")
	if got.TrustState != "changed" {
		t.Fatal(got.TrustState)
	}
}

// This stable fixture detects accidental changes in fingerprint tuple framing.
func TestFingerprintVector(t *testing.T) {
	enc, _ := hex.DecodeString("000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f")
	sig := make(ed25519.PublicKey, 32)
	for i := range sig {
		sig[i] = byte(255 - i)
	}
	got := Fingerprint("01IDENTITY", "01ENC", enc, "01SIGN", sig)
	const want = "2CD2F0C4AEF4CC7FE144D0DC5FA8BECFAD4D346B177DB2A57B5D7D7E34AC78FB"
	if got != want {
		t.Fatalf("update only with a protocol version: %s", got)
	}
}

func TestHPKEAispaceVector(t *testing.T) {
	// Recipient material is from RFC 9180's X25519/HKDF-SHA256/
	// AES-256-GCM base-mode vector; the envelope is the aispace fixture.
	private, _ := hex.DecodeString("497b4502664cfea5d5af0b39934dac72242a74f8480451e1aee7d6a53320333d")
	public, _ := hex.DecodeString("430f4b9859665145a6b1ba274024487bd66f03a2dd577d7753c68d7d7d00c00c")
	rfcPrivate, err := hpke.DHKEM(ecdh.X25519()).NewPrivateKey(private)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(rfcPrivate.PublicKey().Bytes(), public) {
		t.Fatal("RFC 9180 recipient key vector mismatch")
	}
	envelope, _ := hex.DecodeString("799a58a1bb3b2f8b13b928db00f3c45b182aea87cea82e12c9446f6b231c465f6c33c69f5efa1f2cfc1ada4a4d02b257242afce836ab8b5fdc72d225f63d94163b75b35eaf3dacfe44a74a849cd24da7")
	master, err := UnwrapMasterKey(private, "01TRANSFER", "01RECIPIENT", "01KEY", envelope)
	if err != nil {
		t.Fatal(err)
	}
	for i, got := range master {
		if got != byte(i) {
			t.Fatalf("master[%d]=%d", i, got)
		}
	}
}

func TestHPKERFC9180BaseModeVector(t *testing.T) {
	// RFC 9180 Appendix A.1, X25519/HKDF-SHA256/AES-128-GCM base mode.
	private := mustHex(t, "4612c550263fc8ad58375df3f557aac531d26850903e55a9f23f21d8534e8ac8")
	enc := mustHex(t, "37fda3567bdbd628e88668c3c8d7e97d1d1253b6d4ea6d44c150f741f1bf4431")
	info := mustHex(t, "4f6465206f6e2061204772656369616e2055726e")
	privateKey, err := hpke.DHKEM(ecdh.X25519()).NewPrivateKey(private)
	if err != nil {
		t.Fatal(err)
	}
	if got := hex.EncodeToString(privateKey.PublicKey().Bytes()); got != "3948cfe0ad1ddb695d780e59077195da6c56506b027329794ab02bca80815c4d" {
		t.Fatalf("recipient public key = %s", got)
	}
	recipient, err := hpke.NewRecipient(enc, privateKey, hpke.HKDFSHA256(), hpke.AES128GCM(), info)
	if err != nil {
		t.Fatal(err)
	}
	plaintext := mustHex(t, "4265617574792069732074727574682c20747275746820626561757479")
	for sequence, vector := range []struct {
		aad        string
		ciphertext string
	}{
		{"436f756e742d30", "f938558b5d72f1a23810b4be2ab4f84331acc02fc97babc53a52ae8218a355a96d8770ac83d07bea87e13c512a"},
		{"436f756e742d31", "af2d7e9ac9ae7e270f46ba1f975be53c09f8d875bdc8535458c2494e8a6eab251c03d0c22a56b8ca42c2063b84"},
		{"436f756e742d32", "498dfcabd92e8acedc281e85af1cb4e3e31c7dc394a1ca20e173cb72516491588d96a19ad4a683518973dcc180"},
	} {
		got, err := recipient.Open(mustHex(t, vector.aad), mustHex(t, vector.ciphertext))
		if err != nil {
			t.Fatalf("sequence %d: %v", sequence, err)
		}
		if !bytes.Equal(got, plaintext) {
			t.Fatalf("sequence %d plaintext = %x", sequence, got)
		}
	}
	for _, vector := range []struct {
		context string
		want    string
	}{
		{"", "3853fe2b4035195a573ffc53856e77058e15d9ea064de3e59f4961d0095250ee"},
		{"\x00", "2e8f0b54673c7029649d4eb9d5e33bf1872cf76d623ff164ac185da9e88c21a5"},
		{"TestContext", "e9e43065102c3836401bed8c3c3c75ae46be1639869391d62c61f1ec7af54931"},
	} {
		got, err := recipient.Export(vector.context, 32)
		if err != nil {
			t.Fatal(err)
		}
		if hex.EncodeToString(got) != vector.want {
			t.Fatalf("export %q = %x", vector.context, got)
		}
	}
}

func mustHex(t *testing.T, value string) []byte {
	t.Helper()
	b, err := hex.DecodeString(value)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
