package cmd

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aispace-sh/aispace-client/internal/api"
	"github.com/aispace-sh/aispace-client/internal/config"
	identitypkg "github.com/aispace-sh/aispace-client/internal/identity"
)

type sealedTestServer struct {
	mu                 sync.Mutex
	key                string
	claimCap           string
	parts              map[int][]byte
	manifest           []byte
	content            []byte
	downloaded         int64
	committed          bool
	adaptiveDiscovery  bool
	discoveryStatus    int
	transferCreateBody map[string]json.RawMessage
}

func TestSealedTransferCreateAndReceiveEndToEnd(t *testing.T) {
	isolate(t)
	state := &sealedTestServer{key: "ask_validkey0000000000000000000000", parts: make(map[int][]byte)}
	srv := httptest.NewServer(http.HandlerFunc(state.handle))
	t.Cleanup(srv.Close)
	sourceDir := t.TempDir()
	sourceA := filepath.Join(sourceDir, "report.txt")
	sourceB := filepath.Join(sourceDir, "chart.csv")
	if err := os.WriteFile(sourceA, []byte("private report"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sourceB, []byte("x,y\n1,2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	created := run("", "--url", srv.URL, "--key", state.key, "transfer", "create", sourceA, sourceB, "--sealed", "--link", "--max-downloads", "1")
	if created.code != ExitOK {
		t.Fatalf("create: %+v", created)
	}
	var link string
	for _, line := range strings.Split(created.stdout, "\n") {
		if strings.HasPrefix(line, "link    ") {
			link = strings.TrimPrefix(line, "link    ")
		}
	}
	if link == "" || strings.Contains(strings.Join(requestTargets(state), "\n"), "#as1.") {
		t.Fatalf("link=%q request targets=%v", link, requestTargets(state))
	}
	outDir := filepath.Join(t.TempDir(), "received")
	received := run("", "--url", srv.URL, "transfer", "receive", link, "--output", outDir, "--yes")
	if received.code != ExitOK || !strings.Contains(received.stdout, "Verified download") {
		t.Fatalf("receive: %+v", received)
	}
	for name, want := range map[string]string{"report.txt": "private report", "chart.csv": "x,y\n1,2\n"} {
		got, err := os.ReadFile(filepath.Join(outDir, name))
		if err != nil || string(got) != want {
			t.Fatalf("%s=%q err=%v", name, got, err)
		}
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if !state.committed || state.downloaded != int64(len(state.content)) {
		t.Fatalf("committed=%v downloaded=%d content=%d", state.committed, state.downloaded, len(state.content))
	}
}

func TestTransferCreateJSONRequiresExplicitSecretOptIn(t *testing.T) {
	isolate(t)
	runCreate := func(include bool) result {
		state := &sealedTestServer{key: "ask_validkey0000000000000000000000", parts: make(map[int][]byte)}
		server := httptest.NewServer(http.HandlerFunc(state.handle))
		t.Cleanup(server.Close)
		source := filepath.Join(t.TempDir(), "secret.txt")
		if err := os.WriteFile(source, []byte("private report"), 0o600); err != nil {
			t.Fatal(err)
		}
		args := []string{"--url", server.URL, "--key", state.key, "--json", "transfer", "create", source, "--sealed", "--link"}
		if include {
			args = append(args, "--include-secret")
		}
		return run("", args...)
	}

	without := runCreate(false)
	if without.code != ExitOK {
		t.Fatalf("default JSON create: %+v", without)
	}
	var defaultOutput map[string]any
	if err := json.Unmarshal([]byte(without.stdout), &defaultOutput); err != nil {
		t.Fatal(err)
	}
	if _, present := defaultOutput["link"]; present {
		t.Fatal("default JSON exposed bearer link")
	}
	if _, present := defaultOutput["token"]; present {
		t.Fatal("default JSON exposed bearer token")
	}
	if ticketPath, ok := defaultOutput["ticket_file"].(string); !ok || os.Remove(ticketPath) != nil {
		t.Fatalf("could not clear first test ticket: %#v", defaultOutput["ticket_file"])
	}

	with := runCreate(true)
	if with.code != ExitOK {
		t.Fatalf("opt-in JSON create: %+v", with)
	}
	var secretOutput map[string]any
	if err := json.Unmarshal([]byte(with.stdout), &secretOutput); err != nil {
		t.Fatal(err)
	}
	if link, ok := secretOutput["link"].(string); !ok || !strings.Contains(link, "#as1.") {
		t.Fatalf("missing opted-in link: %#v", secretOutput)
	}
	if token, ok := secretOutput["token"].(string); !ok || !strings.HasPrefix(token, "aispace-transfer-v1.") {
		t.Fatalf("missing opted-in token: %#v", secretOutput)
	}
}

func TestAdaptiveCreateDiscoversThenUsesStrictStoredFallback(t *testing.T) {
	isolate(t)
	state := &sealedTestServer{key: "ask_validkey0000000000000000000000", parts: make(map[int][]byte)}
	srv := httptest.NewServer(http.HandlerFunc(state.handle))
	t.Cleanup(srv.Close)
	source := filepath.Join(t.TempDir(), "report.txt")
	if err := os.WriteFile(source, []byte("private report"), 0o600); err != nil {
		t.Fatal(err)
	}
	created := run("", "--url", srv.URL, "--key", state.key, "transfer", "create", source, "--sealed", "--link", "--transport", "adaptive")
	if created.code != ExitOK || !strings.Contains(created.stdout, "adaptive → R2") {
		t.Fatalf("adaptive fallback: %+v", created)
	}
	targets := requestTargets(state)
	if len(targets) < 2 || targets[0] != "/v1/transports" || targets[1] != "/v1/transfers" {
		t.Fatalf("adaptive request order = %v", targets)
	}
	assertStoredCreateBody(t, state.transferCreateBody)
}

func TestAdaptiveCreatePersistsIntentAfterCapableDiscovery(t *testing.T) {
	isolate(t)
	state := &sealedTestServer{key: "ask_validkey0000000000000000000000", parts: make(map[int][]byte), adaptiveDiscovery: true}
	srv := httptest.NewServer(http.HandlerFunc(state.handle))
	t.Cleanup(srv.Close)
	source := filepath.Join(t.TempDir(), "report.txt")
	if err := os.WriteFile(source, []byte("private report"), 0o600); err != nil {
		t.Fatal(err)
	}
	created := run("", "--url", srv.URL, "--key", state.key, "transfer", "create", source, "--sealed", "--link", "--transport", "adaptive")
	if created.code != ExitOK || !strings.Contains(created.stdout, "adaptive → R2 (client or path unsupported)") {
		t.Fatalf("adaptive capable fallback: %+v", created)
	}
	if got := string(state.transferCreateBody["transport_mode"]); got != `"adaptive"` {
		t.Fatalf("transport_mode = %s", got)
	}
	if got := string(state.transferCreateBody["durability_policy"]); got != `"durable_first"` {
		t.Fatalf("durability_policy = %s", got)
	}
}

func TestAdaptiveCreateFallsBackAfterTransientDiscoveryFailure(t *testing.T) {
	isolate(t)
	state := &sealedTestServer{key: "ask_validkey0000000000000000000000", parts: make(map[int][]byte), discoveryStatus: http.StatusInternalServerError}
	srv := httptest.NewServer(http.HandlerFunc(state.handle))
	t.Cleanup(srv.Close)
	source := filepath.Join(t.TempDir(), "report.txt")
	if err := os.WriteFile(source, []byte("private report"), 0o600); err != nil {
		t.Fatal(err)
	}
	created := run("", "--url", srv.URL, "--key", state.key, "transfer", "create", source, "--sealed", "--link", "--transport", "adaptive")
	if created.code != ExitOK || !strings.Contains(created.stdout, "adaptive → R2 (discovery failed)") {
		t.Fatalf("transient discovery fallback: %+v", created)
	}
	assertStoredCreateBody(t, state.transferCreateBody)
}

func assertStoredCreateBody(t *testing.T, body map[string]json.RawMessage) {
	t.Helper()
	if _, exists := body["transport_mode"]; exists {
		t.Fatal("stored compatibility request included transport_mode")
	}
	if _, exists := body["durability_policy"]; exists {
		t.Fatal("stored compatibility request included durability_policy")
	}
}

var sealedTargets sync.Map

func requestTargets(state *sealedTestServer) []string {
	value, _ := sealedTargets.Load(state)
	if value == nil {
		return nil
	}
	return value.([]string)
}

func (s *sealedTestServer) recordTarget(r *http.Request) {
	current := requestTargets(s)
	sealedTargets.Store(s, append(current, r.URL.RequestURI()))
}

func (s *sealedTestServer) handle(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.recordTarget(r)
	w.Header().Set("Content-Type", "application/json")
	body, _ := io.ReadAll(r.Body)
	owner := r.Header.Get("Authorization") == "Bearer "+s.key
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/v1/transports" && owner && s.adaptiveDiscovery:
		_, _ = io.WriteString(w, `{"protocol":"aispace-adaptive-v1","enabled":true,"default_mode":"stored","supported_modes":["stored","adaptive"],"durability_policies":["durable_first"],"negotiation_budget_ms":2000,"candidate_payloads_logged":false,"transports":{"r2":{"available":true,"durable":true,"exposes_peer_address":false},"direct":{"available":false,"durable":false,"exposes_peer_address":true},"turn":{"available":false,"durable":false,"exposes_peer_address":false},"native_relay":{"available":false,"durable":false,"exposes_peer_address":false}},"privacy_modes":["stored_only","relay_only","direct"]}`)
	case r.Method == http.MethodGet && r.URL.Path == "/v1/transports" && owner && s.discoveryStatus != 0:
		w.WriteHeader(s.discoveryStatus)
		_, _ = io.WriteString(w, `{"error":{"code":"internal","message":"temporarily unavailable"}}`)
	case r.Method == http.MethodPost && r.URL.Path == "/v1/transfers" && owner:
		var strict map[string]json.RawMessage
		_ = json.Unmarshal(body, &strict)
		s.transferCreateBody = strict
		if _, exists := strict["transport_mode"]; exists && !s.adaptiveDiscovery {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"error":{"code":"bad_request","message":"unexpected transport_mode"}}`)
			return
		}
		if _, exists := strict["durability_policy"]; exists && !s.adaptiveDiscovery {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"error":{"code":"bad_request","message":"unexpected durability_policy"}}`)
			return
		}
		var in struct {
			ClaimCapability string `json:"claim_capability"`
		}
		_ = json.Unmarshal(body, &in)
		s.claimCap = in.ClaimCapability
		w.WriteHeader(http.StatusCreated)
		if s.adaptiveDiscovery {
			_, _ = io.WriteString(w, `{"id":"01SEALEDTEST","state":"uploading","upload_capability":"upload-cap","revoke_capability":"revoke-cap","created_at":1788881400,"upload_expires_at":1788967800,"expires_at":1788967800,"transport_mode":"adaptive","durability_policy":"durable_first","selected_transport":"r2"}`)
		} else {
			_, _ = io.WriteString(w, `{"id":"01SEALEDTEST","state":"uploading","upload_capability":"upload-cap","revoke_capability":"revoke-cap","created_at":1788881400,"upload_expires_at":1788967800,"expires_at":1788967800}`)
		}
	case r.Method == http.MethodPut && strings.HasPrefix(r.URL.Path, "/v1/transfers/01SEALEDTEST/parts/") && owner && r.Header.Get("X-Upload-Capability") == "upload-cap":
		number, _ := strconv.Atoi(strings.TrimPrefix(r.URL.Path, "/v1/transfers/01SEALEDTEST/parts/"))
		s.parts[number] = append([]byte(nil), body...)
		sum := sha256.Sum256(body)
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprintf(w, `{"part_number":%d,"etag":"etag-%d","size_bytes":%d,"sha256":"%s"}`, number, number, len(body), hex.EncodeToString(sum[:]))
	case r.Method == http.MethodPut && r.URL.Path == "/v1/transfers/01SEALEDTEST/manifest" && owner && r.Header.Get("X-Upload-Capability") == "upload-cap":
		s.manifest = append([]byte(nil), body...)
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprintf(w, `{"manifest_sha256":%q}`, r.Header.Get("X-SHA256"))
	case r.Method == http.MethodPost && r.URL.Path == "/v1/transfers/01SEALEDTEST/complete" && owner:
		var numbers []int
		for number := range s.parts {
			numbers = append(numbers, number)
		}
		sort.Ints(numbers)
		for _, number := range numbers {
			s.content = append(s.content, s.parts[number]...)
		}
		_, _ = io.WriteString(w, `{"id":"01SEALEDTEST","state":"available","expires_at":1788967800}`)
	case r.Method == http.MethodGet && r.URL.Path == "/t/01SEALEDTEST/manifest" && r.Header.Get("Authorization") == "Bearer "+s.claimCap:
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("X-Manifest-SHA256", digestHex(s.manifest))
		_, _ = w.Write(s.manifest)
	case r.Method == http.MethodPost && r.URL.Path == "/t/01SEALEDTEST/claims" && r.Header.Get("Authorization") == "Bearer "+s.claimCap:
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{"id":"claim-1","token":"lease-token","lease_expires_at":1788960000,"downloaded_bytes":0}`)
	case r.Method == http.MethodGet && r.URL.Path == "/t/01SEALEDTEST/content" && r.Header.Get("Authorization") == "Bearer lease-token":
		start := 0
		if raw := strings.TrimPrefix(r.Header.Get("Range"), "bytes="); raw != "" {
			start, _ = strconv.Atoi(strings.TrimSuffix(raw, "-"))
			w.WriteHeader(http.StatusPartialContent)
		}
		_, _ = w.Write(s.content[start:])
	case r.Method == http.MethodPatch && r.URL.Path == "/t/01SEALEDTEST/claims/claim-1" && r.Header.Get("Authorization") == "Bearer lease-token":
		var in struct {
			DownloadedBytes int64 `json:"downloaded_bytes"`
		}
		_ = json.Unmarshal(body, &in)
		s.downloaded = in.DownloadedBytes
		_, _ = fmt.Fprintf(w, `{"id":"claim-1","lease_expires_at":1788960000,"downloaded_bytes":%d}`, s.downloaded)
	case r.Method == http.MethodPost && r.URL.Path == "/t/01SEALEDTEST/claims/claim-1/commit" && r.Header.Get("Authorization") == "Bearer lease-token":
		var in struct {
			ManifestSHA256   string `json:"manifest_sha256"`
			CiphertextSHA256 string `json:"ciphertext_sha256"`
		}
		_ = json.Unmarshal(body, &in)
		if s.downloaded != int64(len(s.content)) || in.ManifestSHA256 != digestHex(s.manifest) || in.CiphertextSHA256 != digestHex(s.content) {
			w.WriteHeader(http.StatusConflict)
			_, _ = io.WriteString(w, `{"error":{"code":"not_verified","message":"digest or progress mismatch"}}`)
			return
		}
		s.committed = true
		_, _ = io.WriteString(w, `{"transfer_id":"01SEALEDTEST","claim_id":"claim-1","status":"verified","actor_kind":"anonymous","committed_at":1788881500}`)
	case r.Method == http.MethodDelete && r.URL.Path == "/t/01SEALEDTEST/claims/claim-1":
		w.WriteHeader(http.StatusNoContent)
	default:
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"error":{"code":"not_found","message":"not found"}}`)
	}
}

func digestHex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func TestAddressedTransferCreateAndInboxReceiveEndToEnd(t *testing.T) {
	isolate(t)
	keys, err := identitypkg.GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	const recipientID = "01ARZ3NDEKTSV4RRFFQ69G5FAV"
	const encID = "01ARZ3NDEKTSV4RRFFQ69G5FAW"
	const sigID = "01ARZ3NDEKTSV4RRFFQ69G5FAX"
	fp := identitypkg.Fingerprint(recipientID, encID, keys.EncryptionPublic, sigID, keys.SigningPublic)
	state := &addressedTestServer{key: "ask_validkey0000000000000000000000", recipientID: recipientID, encID: encID, sigID: sigID, encPub: identitypkg.PublicKeyString(keys.EncryptionPublic), sigPub: identitypkg.PublicKeyString(keys.SigningPublic), fingerprint: fp, parts: map[int][]byte{}}
	srv := httptest.NewServer(http.HandlerFunc(state.handle))
	defer srv.Close()
	writeConfig(t, config.File{Key: state.key, URL: srv.URL})
	store := identitypkg.NewStore(mustConfigPath(t))
	local := identitypkg.NewLocalIdentity(srv.URL, recipientID, "receiver", "Receiver", encID, sigID, time.Now().Unix(), keys)
	if _, err := store.SaveIdentity(local); err != nil {
		t.Fatal(err)
	}
	recipient := identitypkg.Recipient{Alias: "receiver", ServerURL: srv.URL, IdentityID: recipientID, EncryptionKeyID: encID, EncryptionPublicKey: state.encPub, SigningKeyID: sigID, SigningPublicKey: state.sigPub, Fingerprint: fp, TrustState: "pinned", CreatedAt: 1, UpdatedAt: 1}
	if err := store.PutRecipient(recipient); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(t.TempDir(), "report.txt")
	if err := os.WriteFile(src, []byte("addressed secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	created := run("", "transfer", "create", src, "--to", "receiver")
	if created.code != ExitOK || strings.Contains(created.stdout, "/t/") {
		t.Fatalf("create %+v", created)
	}
	deliveryID := state.snapshot().deliveryID
	out := filepath.Join(t.TempDir(), "out")
	blocked := run("", "inbox", "receive", deliveryID, "--identity", recipientID, "--output", out, "--yes")
	if blocked.code != ExitUsage || !strings.Contains(blocked.stderr, "--allow-unknown-sender") {
		t.Fatalf("anonymous automated receive was not blocked: %+v", blocked)
	}
	// Exhaust both net/http's transparent idempotent-request retry and the API
	// client's explicit ambiguous-response retry. The receipt is committed by
	// the fixture before each response is dropped.
	state.setLoseVerifiedResponses(20)
	lost := run("", "inbox", "receive", deliveryID, "--identity", recipientID, "--output", out, "--yes", "--allow-unknown-sender")
	if lost.code == ExitOK || !strings.Contains(lost.stderr, "exact signed replay state was retained") {
		t.Fatalf("lost verified response: %+v", lost)
	}
	verifiedAttempts := 0
	firstSnapshot := state.snapshot()
	for _, receiptType := range firstSnapshot.receipts {
		if receiptType == "verified" {
			verifiedAttempts++
		}
	}
	if verifiedAttempts == 0 {
		t.Fatal("first receive did not submit a verified receipt")
	}
	state.setLoseVerifiedResponses(0)
	manifestReads, contentReads := firstSnapshot.manifestReads, firstSnapshot.contentReads
	received := run("", "inbox", "receive", deliveryID, "--identity", recipientID, "--output", out, "--yes", "--allow-unknown-sender")
	if received.code != ExitOK || !strings.Contains(received.stdout, "already saved") {
		t.Fatalf("restart recovery %+v", received)
	}
	finalSnapshot := state.snapshot()
	if finalSnapshot.manifestReads != manifestReads || finalSnapshot.contentReads != contentReads {
		t.Fatalf("restart re-downloaded output: manifest %d->%d content %d->%d", manifestReads, finalSnapshot.manifestReads, contentReads, finalSnapshot.contentReads)
	}
	got, err := os.ReadFile(filepath.Join(out, "report.txt"))
	if err != nil || string(got) != "addressed secret" {
		t.Fatalf("got %q err=%v", got, err)
	}
	wantReceipts := append([]string{"downloaded"}, make([]string, verifiedAttempts+1)...)
	for i := 1; i < len(wantReceipts); i++ {
		wantReceipts[i] = "verified"
	}
	if strings.Join(finalSnapshot.receipts, ",") != strings.Join(wantReceipts, ",") {
		t.Fatalf("receipts %v", finalSnapshot.receipts)
	}
	var pending pendingInboxReceiptSequence
	if err := store.LoadPending("receipt-receive", deliveryID, &pending); !os.IsNotExist(err) {
		t.Fatalf("receipt replay state remained after recovery: %v", err)
	}
	if _, err := store.LoadDeliveryContext(deliveryID); err != nil {
		t.Fatalf("verified delivery context was not saved: %v", err)
	}
}

type addressedTestServer struct {
	mu                                                                                            sync.Mutex
	key, recipientID, encID, sigID, encPub, sigPub, fingerprint, deliveryID, wrapped, manifestSHA string
	ciphertextSHA                                                                                 string
	declaredPlaintextBytes                                                                        int64
	fileCount                                                                                     int
	alsoLink                                                                                      bool
	parts                                                                                         map[int][]byte
	manifest, content                                                                             []byte
	receipts                                                                                      []string
	loseVerifiedResponses                                                                         int
	manifestReads, contentReads                                                                   int
}

type addressedTestSnapshot struct {
	deliveryID                  string
	receipts                    []string
	manifestReads, contentReads int
}

func (s *addressedTestServer) snapshot() addressedTestSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	return addressedTestSnapshot{
		deliveryID:    s.deliveryID,
		receipts:      append([]string(nil), s.receipts...),
		manifestReads: s.manifestReads,
		contentReads:  s.contentReads,
	}
}

func (s *addressedTestServer) setLoseVerifiedResponses(n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loseVerifiedResponses = n
}

func (s *addressedTestServer) handle(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	body, _ := io.ReadAll(r.Body)
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/i/"+s.recipientID:
		_, _ = fmt.Fprintf(w, `{"protocol":"aispace-identity-v1","id":%q,"handle":"receiver","display_name":"Receiver","created_at":1,"fingerprint":%q,"encryption_key":{"protocol":"aispace-identity-v1","identity_id":%q,"key_id":%q,"purpose":"encryption","algorithm":"X25519","public_key":%q,"created_at":1,"not_after":null,"previous_key_id":null,"revoked_at":null},"signing_key":{"protocol":"aispace-identity-v1","identity_id":%q,"key_id":%q,"purpose":"signing","algorithm":"Ed25519","public_key":%q,"created_at":1,"not_after":null,"previous_key_id":null,"revoked_at":null}}`, s.recipientID, s.fingerprint, s.recipientID, s.encID, s.encPub, s.recipientID, s.sigID, s.sigPub)
	case r.Method == http.MethodPost && r.URL.Path == "/v1/transfers":
		var in api.TransferCreateRequest
		_ = json.Unmarshal(body, &in)
		s.declaredPlaintextBytes = in.DeclaredPlaintextBytes
		s.fileCount = in.FileCount
		w.WriteHeader(201)
		_, _ = fmt.Fprintf(w, `{"id":"01ARZ3NDEKTSV4RRFFQ69G5FAY","state":"uploading","upload_capability":"up","revoke_capability":"rv","declared_plaintext_bytes":%d,"file_count":%d,"max_downloads":null,"created_at":1788881400,"upload_expires_at":1788967800,"expires_at":1788967800}`, in.DeclaredPlaintextBytes, in.FileCount)
	case r.Method == http.MethodPut && strings.Contains(r.URL.Path, "/parts/"):
		n, _ := strconv.Atoi(filepath.Base(r.URL.Path))
		s.parts[n] = append([]byte(nil), body...)
		_, _ = fmt.Fprintf(w, `{"part_number":%d,"etag":"e","size_bytes":%d,"sha256":%q}`, n, len(body), digestHex(body))
	case r.Method == http.MethodPut && strings.HasSuffix(r.URL.Path, "/manifest"):
		s.manifest = append([]byte(nil), body...)
		s.manifestSHA = digestHex(body)
		_, _ = fmt.Fprintf(w, `{"manifest_sha256":%q}`, s.manifestSHA)
	case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/deliveries"):
		var in map[string]any
		_ = json.Unmarshal(body, &in)
		s.wrapped = in["wrapped_master_key"].(string)
		s.alsoLink, _ = in["also_link"].(bool)
		s.deliveryID = "01ARZ3NDEKTSV4RRFFQ69G5FAZ"
		w.WriteHeader(201)
		_, _ = fmt.Fprintf(w, `{"delivery":{"id":%q,"transfer_id":"01ARZ3NDEKTSV4RRFFQ69G5FAY","recipient_identity_id":%q,"recipient_key_id":%q,"wrapped_master_key":%q,"manifest_sha256":%q,"also_link":%t,"expires_at":1788967800}}`, s.deliveryID, s.recipientID, s.encID, s.wrapped, s.manifestSHA, s.alsoLink)
	case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/complete"):
		var complete map[string]string
		_ = json.Unmarshal(body, &complete)
		s.ciphertextSHA = complete["ciphertext_sha256"]
		var nums []int
		for n := range s.parts {
			nums = append(nums, n)
		}
		sort.Ints(nums)
		for _, n := range nums {
			s.content = append(s.content, s.parts[n]...)
		}
		_, _ = fmt.Fprintf(w, `{"id":"01ARZ3NDEKTSV4RRFFQ69G5FAY","state":"available","declared_plaintext_bytes":%d,"ciphertext_bytes":%d,"file_count":%d,"manifest_size_bytes":%d,"manifest_sha256":%q,"ciphertext_sha256":%q,"max_downloads":null,"created_at":1788881400,"expires_at":1788967800}`, s.declaredPlaintextBytes, len(s.content), s.fileCount, len(s.manifest), s.manifestSHA, s.ciphertextSHA)
	case r.Method == http.MethodGet && r.URL.Path == "/v1/inbox":
		_, _ = fmt.Fprintf(w, `{"deliveries":[{"id":%q,"transfer_id":"01ARZ3NDEKTSV4RRFFQ69G5FAY","recipient_identity_id":%q,"recipient_key_id":%q,"wrapped_master_key":%q,"manifest_sha256":%q,"also_link":%t,"expires_at":1788967800}],"next_cursor":null}`, s.deliveryID, s.recipientID, s.encID, s.wrapped, s.manifestSHA, s.alsoLink)
	case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/claims"):
		w.WriteHeader(201)
		_, _ = fmt.Fprintf(w, `{"claim_id":"01ARZ3NDEKTSV4RRFFQ69G5FB0","claim_nonce":"nonce","lease_expires_at":1999999999,"delivery":{"id":%q,"transfer_id":"01ARZ3NDEKTSV4RRFFQ69G5FAY","recipient_identity_id":%q,"recipient_key_id":%q,"wrapped_master_key":%q,"manifest_sha256":%q,"also_link":%t,"expires_at":1788967800},"transfer":{"id":"01ARZ3NDEKTSV4RRFFQ69G5FAY","declared_plaintext_bytes":%d,"ciphertext_bytes":%d,"file_count":%d,"manifest_size_bytes":%d,"manifest_sha256":%q,"ciphertext_sha256":%q,"max_downloads":null,"created_at":1788881400,"expires_at":1788967800,"also_link":%t},"sender_signing_key":null,"sender_identity_state":null}`, s.deliveryID, s.recipientID, s.encID, s.wrapped, s.manifestSHA, s.alsoLink, s.declaredPlaintextBytes, len(s.content), s.fileCount, len(s.manifest), s.manifestSHA, s.ciphertextSHA, s.alsoLink)
	case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/manifest"):
		s.manifestReads++
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write(s.manifest)
	case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/content"):
		s.contentReads++
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write(s.content)
	case r.Method == http.MethodPatch && strings.Contains(r.URL.Path, "/claims/"):
		_, _ = fmt.Fprintf(w, `{"claim_id":"01ARZ3NDEKTSV4RRFFQ69G5FB0","claim_nonce":"nonce","lease_expires_at":1999999999,"delivery":{"id":%q,"transfer_id":"01ARZ3NDEKTSV4RRFFQ69G5FAY","recipient_identity_id":%q,"recipient_key_id":%q},"transfer":{"id":"01ARZ3NDEKTSV4RRFFQ69G5FAY"}}`, s.deliveryID, s.recipientID, s.encID)
	case r.Method == http.MethodPost && (strings.HasSuffix(r.URL.Path, "/receipts") || strings.HasSuffix(r.URL.Path, "/reject")):
		var in struct {
			Receipt struct {
				Type string `json:"type"`
			} `json:"receipt"`
		}
		_ = json.Unmarshal(body, &in)
		s.receipts = append(s.receipts, in.Receipt.Type)
		if in.Receipt.Type == "verified" && s.loseVerifiedResponses > 0 {
			s.loseVerifiedResponses--
			conn, _, err := w.(http.Hijacker).Hijack()
			if err != nil {
				panic(err)
			}
			_ = conn.Close()
			return
		}
		w.WriteHeader(201)
		_, _ = fmt.Fprintf(w, `{"receipt":{"id":"01ARZ3NDEKTSV4RRFFQ69G5FB1","delivery_id":%q,"type":%q,"actor_kind":"identity","manifest_sha256":%q,"event_at":1,"received_at":1}}`, s.deliveryID, in.Receipt.Type, s.manifestSHA)
	default:
		w.WriteHeader(404)
		_, _ = io.WriteString(w, `{"error":{"code":"not_found","message":"not found"}}`)
	}
}
