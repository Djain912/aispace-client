package handoff

import (
	"bytes"
	"strings"
	"testing"

	"github.com/aispace-sh/aispace-client/internal/sealed"
)

func testIntent(t *testing.T) Intent {
	t.Helper()
	var s sealed.Secrets
	copy(s.MasterKey[:], bytes.Repeat([]byte{0x11}, 32))
	copy(s.ClaimCapability[:], bytes.Repeat([]byte{0x22}, 32))
	i, err := FromSealed("https://aispace.sh", "01TESTTRANSFER", s)
	if err != nil {
		t.Fatal(err)
	}
	return i
}

func TestRepresentationsRoundTripIdentically(t *testing.T) {
	want := testIntent(t)
	token, _ := want.Token()
	const golden = "aispace-transfer-v1.QVNIVAEBAQASaHR0cHM6Ly9haXNwYWNlLnNoAgAOMDFURVNUVFJBTlNGRVIDACAREREREREREREREREREREREREREREREREREREREREREQQAICIiIiIiIiIiIiIiIiIiIiIiIiIiIiIiIiIiIiIiIiIi2F-Y0ryE_pQ"
	if token != golden {
		t.Fatalf("token changed:\n got %s\nwant %s", token, golden)
	}
	deep, _ := want.DeepLink()
	https, _ := want.HTTPSLink()
	for _, input := range []string{token, deep, https} {
		got, err := Parse(input, "https://aispace.sh")
		if err != nil {
			t.Fatalf("parse %q: %v", input[:min(len(input), 30)], err)
		}
		if got.Version != want.Version || got.Mode != want.Mode || got.ServiceOrigin != want.ServiceOrigin || got.TransferID != want.TransferID || !bytes.Equal(got.MasterKey, want.MasterKey) || !bytes.Equal(got.ClaimCapability, want.ClaimCapability) {
			t.Fatalf("round trip mismatch: %#v", got)
		}
	}
}

func TestRejectsDamageUnknownFieldsAndOriginConfusion(t *testing.T) {
	i := testIntent(t)
	token, _ := i.Token()
	badChecksum := token[:len(token)-1] + map[bool]string{true: "A", false: "B"}[strings.HasSuffix(token, "B")]
	for _, input := range []string{
		badChecksum,
		"https://evil.example/t/01TESTTRANSFER#as1." + strings.Repeat("A", 43) + "." + strings.Repeat("A", 43),
		"aispace://transfer/v2/nope",
		"https://aispace.sh/t/../t/x#nope",
	} {
		if _, err := Parse(input, "https://aispace.sh"); err == nil {
			t.Fatalf("accepted invalid input %q", input)
		}
	}
}

func TestTokenDoesNotExposeSecretsInStringSummary(t *testing.T) {
	i := testIntent(t)
	token, _ := i.Token()
	if strings.Contains(i.String(), token) || strings.Contains(i.String(), "ERERER") {
		t.Fatal("summary exposed secret")
	}
}

func TestCanonicalOriginTreatsHostCaseAndDefaultPortsAsEquivalent(t *testing.T) {
	for input, want := range map[string]string{
		"https://EXAMPLE.com:443/":    "https://example.com",
		"http://LOCALHOST:80/":        "http://localhost",
		"http://[::1]:80/":            "http://[::1]",
		"http://[::1]:8787/":          "http://[::1]:8787",
		"https://[2001:db8::1]:443/":  "https://[2001:db8::1]",
		"https://[2001:db8::1]:8443/": "https://[2001:db8::1]:8443",
	} {
		got, err := CanonicalOrigin(input)
		if err != nil || got != want {
			t.Fatalf("CanonicalOrigin(%q) = %q, %v", input, got, err)
		}
	}
	i, err := FromSealed("https://EXAMPLE.com:443/", "01TESTTRANSFER", sealed.Secrets{MasterKey: [32]byte{1}, ClaimCapability: [32]byte{2}})
	if err != nil || i.ServiceOrigin != "https://example.com" {
		t.Fatalf("FromSealed = %#v, %v", i, err)
	}
}
