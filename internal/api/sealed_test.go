package api

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

type sealedRoundTripFunc func(*http.Request) (*http.Response, error)

func (f sealedRoundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func response(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}

func TestSealedOwnerAPIContract(t *testing.T) {
	var requests []*http.Request
	var bodies [][]byte
	c := New("https://example.test", "ask_owner", "test")
	c.HTTP = &http.Client{Transport: sealedRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(r.Body)
		requests = append(requests, r.Clone(r.Context()))
		bodies = append(bodies, body)
		switch r.URL.Path {
		case "/v1/transfers":
			return response(201, `{"id":"T1","upload_capability":"up","revoke_capability":"rv","created_at":1,"expires_at":2}`), nil
		case "/v1/transfers/T1/parts/1":
			return response(200, `{"part_number":1,"etag":"e","size_bytes":3,"sha256":"abc"}`), nil
		case "/v1/transfers/T1/manifest":
			return response(200, `{"manifest_sha256":"def"}`), nil
		case "/v1/transfers/T1/complete":
			return response(200, `{"id":"T1","state":"available"}`), nil
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
			return nil, nil
		}
	})}
	max := int64(1)
	created, err := c.CreateTransfer(context.Background(), TransferCreateRequest{Protocol: "aispace-sealed-v1", MaxDownloads: &max, ClaimCapability: "claim-secret"}, "create-key")
	if err != nil || created.Value.ID != "T1" {
		t.Fatalf("create=%+v err=%v", created.Value, err)
	}
	part, err := c.UploadTransferPart(context.Background(), "T1", 1, bytes.NewReader([]byte("abc")), 3, "abc", "upload-secret", "part-key")
	if err != nil || part.Value.ETag != "e" {
		t.Fatalf("part=%+v err=%v", part.Value, err)
	}
	if err := c.UploadTransferManifest(context.Background(), "T1", bytes.NewReader([]byte("manifest")), 8, "def", "upload-secret", "manifest-key"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.CompleteTransfer(context.Background(), "T1", "def", "456", "upload-secret", "complete-key"); err != nil {
		t.Fatal(err)
	}
	if requests[0].Header.Get("Authorization") != "Bearer ask_owner" || !bytes.Contains(bodies[0], []byte(`"claim_capability":"claim-secret"`)) {
		t.Fatalf("create request headers=%v body=%s", requests[0].Header, bodies[0])
	}
	for _, i := range []int{1, 2, 3} {
		if requests[i].Header.Get("X-Upload-Capability") != "upload-secret" {
			t.Fatalf("request %d omitted upload capability", i)
		}
	}
	if requests[1].ContentLength != 3 || requests[1].Header.Get("X-SHA256") != "abc" || string(bodies[1]) != "abc" {
		t.Fatalf("part request len=%d headers=%v body=%q", requests[1].ContentLength, requests[1].Header, bodies[1])
	}
}

func TestSealedPublicAPIUsesOnlyProvidedBearer(t *testing.T) {
	const masterSecret = "MASTER_MUST_NOT_LEAK"
	var requests []*http.Request
	c := New("https://example.test", "", "test")
	c.HTTP = &http.Client{Transport: sealedRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		requests = append(requests, r.Clone(r.Context()))
		switch {
		case strings.HasSuffix(r.URL.Path, "/manifest"):
			return response(200, "envelope"), nil
		case strings.HasSuffix(r.URL.Path, "/claims") && r.Method == http.MethodPost:
			return response(201, `{"id":"C1","token":"lease","state":"active","lease_expires_at":2}`), nil
		case strings.HasSuffix(r.URL.Path, "/content"):
			resp := response(206, "ciphertext")
			return resp, nil
		default:
			t.Fatalf("unexpected %s %s", r.Method, r.URL.Path)
			return nil, nil
		}
	})}
	manifest, err := c.GetTransferManifest(context.Background(), "T1", "claim-cap")
	if err != nil {
		t.Fatal(err)
	}
	_ = manifest.Body.Close()
	claim, err := c.ClaimTransfer(context.Background(), "T1", "claim-cap", "claim-key")
	if err != nil {
		t.Fatal(err)
	}
	content, err := c.DownloadTransferContent(context.Background(), "T1", claim.Value.Token, "bytes=10-")
	if err != nil {
		t.Fatal(err)
	}
	_ = content.Body.Close()
	if requests[0].Header.Get("Authorization") != "Bearer claim-cap" || requests[1].Header.Get("Authorization") != "Bearer claim-cap" || requests[2].Header.Get("Authorization") != "Bearer lease" {
		t.Fatalf("authorization headers not scoped: %q %q %q", requests[0].Header.Get("Authorization"), requests[1].Header.Get("Authorization"), requests[2].Header.Get("Authorization"))
	}
	for _, req := range requests {
		if strings.Contains(req.URL.String(), "claim-cap") || strings.Contains(req.URL.String(), masterSecret) || strings.Contains(req.Header.Get("Authorization"), masterSecret) {
			t.Fatalf("secret leaked in request: %s headers=%v", req.URL, req.Header)
		}
	}
}
