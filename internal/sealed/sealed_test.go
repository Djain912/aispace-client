package sealed

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func vectorMaster() [32]byte {
	var master [32]byte
	for i := range master {
		master[i] = byte(i)
	}
	return master
}

func TestDerivationAndChunkGoldenVector(t *testing.T) {
	const transferID = "01JSEALEDVECTOR00000000000"
	k, err := deriveKeys(vectorMaster(), transferID)
	if err != nil {
		t.Fatal(err)
	}
	if got := hex.EncodeToString(k.manifest[:]); got != "8c768aedc9410f372ed35b8a8a99ce78bb024481cdcea6b07b832bb75b22d8db" {
		t.Fatalf("manifest key = %s", got)
	}
	if got := hex.EncodeToString(k.data[:]); got != "7f4f340fc787f43b51940c05e17288baed030db188447f2b82781628c6d27590" {
		t.Fatalf("data key = %s", got)
	}
	if got := hex.EncodeToString(k.noncePrefix[:]); got != "ebcf6995" {
		t.Fatalf("nonce prefix = %s", got)
	}
	ciphertext, err := EncryptChunk(vectorMaster(), transferID, 2, 7, 1, []byte("hello"))
	if err != nil {
		t.Fatal(err)
	}
	if got := hex.EncodeToString(ciphertext); got != "968cb9a832d1a376162c80045341ba645b4a08588d" {
		t.Fatalf("ciphertext = %s", got)
	}
	plain, err := DecryptChunk(vectorMaster(), transferID, 2, 7, 1, 5, ciphertext)
	if err != nil || string(plain) != "hello" {
		t.Fatalf("decrypt = %q, %v", plain, err)
	}
	ciphertext[0] ^= 1
	if _, err := DecryptChunk(vectorMaster(), transferID, 2, 7, 1, 5, ciphertext); err == nil {
		t.Fatal("tampered ciphertext authenticated")
	}
}

func TestEmptyChunkAndManifestRoundTrip(t *testing.T) {
	const transferID = "01JSEALEDVECTOR00000000000"
	ct, err := EncryptChunk(vectorMaster(), transferID, 0, 0, 0, nil)
	if err != nil || len(ct) != 16 {
		t.Fatalf("empty chunk len=%d err=%v", len(ct), err)
	}
	m := Manifest{
		Protocol: Protocol, TransferID: transferID, CreatedAt: 1, ExpiresAt: 2,
		CiphertextSHA256: SHA256Hex(ct), Sender: nil, Signature: nil,
		Files: []ManifestFile{{
			Index: 0, Path: "empty.txt", ContentType: "text/plain", SizeBytes: 0,
			SHA256: SHA256Hex(nil), Chunks: []ManifestChunk{{GlobalIndex: 0, PlaintextBytes: 0}},
		}},
	}
	canonical, err := CanonicalManifest(m)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(canonical), "\\u003c") || canonical[len(canonical)-1] == '\n' {
		t.Fatalf("non-canonical JSON: %q", canonical)
	}
	envelope, err := EncryptManifest(vectorMaster(), transferID, canonical)
	if err != nil {
		t.Fatal(err)
	}
	plain, err := DecryptManifest(vectorMaster(), transferID, envelope)
	if err != nil || !bytes.Equal(plain, canonical) {
		t.Fatalf("manifest decrypt mismatch: %v", err)
	}
	decoded, err := DecodeManifest(plain)
	if err != nil || !reflect.DeepEqual(decoded, m) {
		t.Fatalf("decoded=%+v err=%v", decoded, err)
	}
}

func TestReferenceRoundTripAndStrictParsing(t *testing.T) {
	var s Secrets
	for i := range s.MasterKey {
		s.MasterKey[i] = byte(i)
		s.ClaimCapability[i] = byte(255 - i)
	}
	token, err := s.CLIToken("01TEST")
	if err != nil {
		t.Fatal(err)
	}
	id, got, err := ParseReference(token)
	if err != nil || id != "01TEST" || got != s {
		t.Fatalf("token round trip id=%q got=%v err=%v", id, got, err)
	}
	id, got, err = ParseReference("https://aispace.sh/t/01TEST#" + s.Fragment())
	if err != nil || id != "01TEST" || got != s {
		t.Fatalf("link round trip id=%q got=%v err=%v", id, got, err)
	}
	for _, bad := range []string{s.Fragment() + ".extra", "http://example.com/t/01TEST#" + s.Fragment(), "as1.short.short"} {
		if _, _, err := ParseReference(bad); err == nil {
			t.Errorf("accepted invalid reference %q", bad)
		}
	}
}

func TestManifestEnvelopeSizeBoundary(t *testing.T) {
	master := vectorMaster()
	plain := bytes.Repeat([]byte{'x'}, MaxManifestBytes)
	envelope, err := EncryptManifest(master, "01JSEALEDVECTOR00000000000", plain)
	if err != nil {
		t.Fatal(err)
	}
	if len(envelope) != MaxManifestEnvelopeBytes {
		t.Fatalf("envelope size=%d, want %d", len(envelope), MaxManifestEnvelopeBytes)
	}
	if _, err := EncryptManifest(master, "01JSEALEDVECTOR00000000000", append(plain, 'x')); err == nil {
		t.Fatal("oversized plaintext manifest was accepted")
	}
	if _, err := DecryptManifest(master, "01JSEALEDVECTOR00000000000", append(envelope, 0)); err == nil {
		t.Fatal("oversized manifest envelope was accepted")
	}
}

type readCloser struct{ io.Reader }

func (r readCloser) Close() error { return nil }

type failAfterReader struct {
	data []byte
	done bool
}

func (r *failAfterReader) Read(p []byte) (int, error) {
	if r.done {
		return 0, errors.New("forced disconnect")
	}
	r.done = true
	n := copy(p, r.data[:min(3, len(r.data))])
	return n, nil
}

func TestReceiveBundleRetriesAtChunkBoundaryAndVerifies(t *testing.T) {
	master := vectorMaster()
	const transferID = "01JSEALEDVECTOR00000000000"
	files := []struct {
		name string
		data []byte
	}{{"a.txt", []byte("hello")}, {"b.txt", []byte("world!")}}
	var ciphertext []byte
	var manifestFiles []ManifestFile
	for i, f := range files {
		ct, err := EncryptChunk(master, transferID, i, uint64(i), 0, f.data)
		if err != nil {
			t.Fatal(err)
		}
		ciphertext = append(ciphertext, ct...)
		manifestFiles = append(manifestFiles, ManifestFile{
			Index: i, Path: f.name, ContentType: "text/plain", SizeBytes: int64(len(f.data)), SHA256: SHA256Hex(f.data),
			Chunks: []ManifestChunk{{GlobalIndex: uint64(i), PlaintextBytes: int64(len(f.data))}},
		})
	}
	m := Manifest{Protocol: Protocol, TransferID: transferID, CreatedAt: 1, ExpiresAt: 2, CiphertextSHA256: SHA256Hex(ciphertext), Files: manifestFiles}
	dir := t.TempDir()
	opens := 0
	err := ReceiveBundle(context.Background(), m, master, func(_ context.Context, offset int64) (io.ReadCloser, error) {
		opens++
		if opens == 1 {
			return readCloser{&failAfterReader{data: ciphertext[offset:]}}, nil
		}
		return readCloser{bytes.NewReader(ciphertext[offset:])}, nil
	}, ReceiveOptions{OutputDir: dir, Retries: 1})
	if err != nil {
		t.Fatal(err)
	}
	if opens != 2 {
		t.Fatalf("opens=%d, want 2", opens)
	}
	for _, f := range files {
		got, err := os.ReadFile(filepath.Join(dir, f.name))
		if err != nil || !bytes.Equal(got, f.data) {
			t.Fatalf("%s=%q err=%v", f.name, got, err)
		}
	}
	if err := ReceiveBundle(context.Background(), m, master, func(context.Context, int64) (io.ReadCloser, error) {
		return readCloser{bytes.NewReader(ciphertext)}, nil
	}, ReceiveOptions{OutputDir: dir}); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("existing output err=%v", err)
	}
}

func TestReceiveBundleOverwriteRollsBackOnInstallFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("removing an open partial file is not supported on Windows")
	}
	master := vectorMaster()
	const transferID = "01JSEALEDVECTOR00000000000"
	files := []struct {
		name string
		data []byte
	}{{"a.txt", []byte("new-a")}, {"b.txt", []byte("new-b")}}
	var ciphertext []byte
	var manifestFiles []ManifestFile
	for i, file := range files {
		chunk, err := EncryptChunk(master, transferID, i, uint64(i), 0, file.data)
		if err != nil {
			t.Fatal(err)
		}
		ciphertext = append(ciphertext, chunk...)
		manifestFiles = append(manifestFiles, ManifestFile{
			Index: i, Path: file.name, ContentType: "text/plain", SizeBytes: int64(len(file.data)), SHA256: SHA256Hex(file.data),
			Chunks: []ManifestChunk{{GlobalIndex: uint64(i), PlaintextBytes: int64(len(file.data))}},
		})
	}
	manifest := Manifest{Protocol: Protocol, TransferID: transferID, CreatedAt: 1, ExpiresAt: 2, CiphertextSHA256: SHA256Hex(ciphertext), Files: manifestFiles}
	dir := t.TempDir()
	for _, file := range files {
		if err := os.WriteFile(filepath.Join(dir, file.name), []byte("original-"+file.name), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	err := ReceiveBundle(context.Background(), manifest, master, func(context.Context, int64) (io.ReadCloser, error) {
		return readCloser{bytes.NewReader(ciphertext)}, nil
	}, ReceiveOptions{
		OutputDir: dir,
		Overwrite: true,
		Progress: func(received int64) error {
			if received == int64(len(ciphertext)) {
				return os.Remove(filepath.Join(dir, "b.txt.partial"))
			}
			return nil
		},
	})
	if err == nil {
		t.Fatal("forced install failure unexpectedly succeeded")
	}
	for _, file := range files {
		got, readErr := os.ReadFile(filepath.Join(dir, file.name))
		if readErr != nil || string(got) != "original-"+file.name {
			t.Fatalf("%s=%q err=%v", file.name, got, readErr)
		}
	}
}
