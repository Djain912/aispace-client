package cmd

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/aispace-sh/aispace-client/internal/adaptive"
	"github.com/aispace-sh/aispace-client/internal/api"
	"github.com/aispace-sh/aispace-client/internal/duration"
	"github.com/aispace-sh/aispace-client/internal/handoff"
	identitypkg "github.com/aispace-sh/aispace-client/internal/identity"
	"github.com/aispace-sh/aispace-client/internal/sealed"
	"golang.org/x/term"
)

const transferTokenEnv = "AISPACE_TRANSFER_TOKEN"

type transferTicket struct {
	Version              int      `json:"version"`
	TransferID           string   `json:"transfer_id"`
	ServerURL            string   `json:"server_url"`
	UploadCapability     string   `json:"upload_capability"`
	RevokeCapability     string   `json:"revoke_capability"`
	RecipientToken       string   `json:"recipient_token"`
	CiphertextSHA256     string   `json:"ciphertext_sha256"`
	ManifestSHA256       string   `json:"manifest_sha256"`
	SourcePaths          []string `json:"source_paths,omitempty"`
	CreatedAt            int64    `json:"created_at"`
	ExpiresAt            int64    `json:"expires_at"`
	RecipientIdentityID  string   `json:"recipient_identity_id,omitempty"`
	RecipientKeyID       string   `json:"recipient_key_id,omitempty"`
	RecipientFingerprint string   `json:"recipient_fingerprint,omitempty"`
	WrappedMasterKey     string   `json:"wrapped_master_key,omitempty"`
	SenderIdentityID     string   `json:"sender_identity_id,omitempty"`
	SenderSigningKeyID   string   `json:"sender_signing_key_id,omitempty"`
	AlsoLink             bool     `json:"also_link,omitempty"`
	TransportMode        string   `json:"transport_mode,omitempty"`
	DurabilityPolicy     string   `json:"durability_policy,omitempty"`
	SelectedTransport    string   `json:"selected_transport,omitempty"`
}

type addressedUpload struct {
	Recipient        identitypkg.Recipient
	Sender           *identitypkg.LocalIdentity
	WrappedMasterKey string
	AlsoLink         bool
}

func (a *app) transferCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "transfer",
		Short: "Create and receive end-to-end encrypted asynchronous transfers",
		Args:  noArgs,
	}
	cmd.AddCommand(a.transferCreateCmd(), a.transferResumeCmd(), a.transferReceiveCmd(), a.transferStatusCmd(), a.transferRevokeCmd())
	return cmd
}

func (a *app) transferCreateCmd() *cobra.Command {
	var expires string
	var maxDownloads int64
	var sealedFlag, linkFlag, alsoLink, trustOnFirstUse, includeSecret bool
	var recipientRef, recipientFingerprint, senderRef, transportMode, durabilityPolicy, privacyMode string
	cmd := &cobra.Command{
		Use:   "create <path> [path...] --sealed --link [--expires 1d] [--max-downloads 1]",
		Short: "Encrypt one or more files locally and publish one resumable transfer",
		Long: "Inspects and hashes regular files, reserves a transfer, encrypts an opaque manifest\n" +
			"and independently authenticated chunks locally, then uploads ciphertext. The printed\n" +
			"link fragment contains the decryption secret and is never sent to aispace.",
		Example: "  aispace transfer create report.pdf charts.png --sealed --link --expires 1d --max-downloads 1",
		Args:    minArgs(1, "one or more regular file paths"),
		RunE: func(cmd *cobra.Command, args []string) error {
			if includeSecret && !a.jsonOut {
				return usagef("--include-secret requires --json")
			}
			if includeSecret && outputIsTerminal(a.stdout) {
				return usagef("refusing to emit a transfer bearer secret to a terminal; redirect JSON to a mode-0600 file")
			}
			if !sealedFlag || (recipientRef == "" && !linkFlag) {
				return usagef("sealed transfer creation requires --sealed and either --link or --to")
			}
			if recipientRef != "" && linkFlag && !alsoLink {
				return usagef("addressed delivery does not create a public secret link; use --also-link explicitly")
			}
			if recipientRef == "" && (alsoLink || senderRef != "" || trustOnFirstUse || recipientFingerprint != "") {
				return usagef("--also-link, --from, and recipient trust flags require --to")
			}
			if maxDownloads < 0 {
				return usagef("--max-downloads must be >= 0")
			}
			transportRequest := adaptive.Request{Mode: transportMode, Durability: durabilityPolicy, Privacy: privacyMode}
			if err := adaptive.Validate(transportRequest); err != nil {
				return usagef("%v", err)
			}
			var expiresIn int64
			var err error
			if expires != "" {
				expiresIn, err = duration.Seconds(expires)
				if err != nil {
					return usagef("--expires: %v", err)
				}
			}
			return a.runTransferCreate(cmd.Context(), args, expiresIn, maxDownloads, recipientRef, recipientFingerprint, senderRef, trustOnFirstUse, alsoLink, includeSecret, transportRequest)
		},
	}
	cmd.Flags().BoolVar(&sealedFlag, "sealed", true, "use aispace-sealed-v1 local encryption (required)")
	cmd.Flags().BoolVar(&linkFlag, "link", false, "produce an anonymous fragment-secret recipient link")
	cmd.Flags().StringVar(&recipientRef, "to", "", "address delivery to a pinned recipient alias or identity ID")
	cmd.Flags().StringVar(&recipientFingerprint, "recipient-fingerprint", "", "expected recipient fingerprint (pins exact current keys)")
	cmd.Flags().BoolVar(&trustOnFirstUse, "trust-on-first-use", false, "explicitly pin the recipient's current keys on first use")
	cmd.Flags().StringVar(&senderRef, "from", "", "sign the private manifest with this local identity")
	cmd.Flags().BoolVar(&alsoLink, "also-link", false, "also print an anonymous fragment-secret link for an addressed delivery")
	cmd.Flags().BoolVar(&includeSecret, "include-secret", false, "include bearer link and token in redirected JSON output (never a terminal)")
	cmd.Flags().StringVar(&expires, "expires", "", "transfer lifetime, e.g. 30m, 1d, 7d (default: server policy)")
	cmd.Flags().Int64Var(&maxDownloads, "max-downloads", 0, "verified download cap (default: unlimited)")
	cmd.Flags().StringVar(&transportMode, "transport", adaptive.ModeStored, "transport mode: stored or adaptive")
	cmd.Flags().StringVar(&durabilityPolicy, "durability", adaptive.DurableFirst, "durability policy (currently durable-first)")
	cmd.Flags().StringVar(&privacyMode, "transport-privacy", adaptive.PrivacyRelayOnly, "live privacy: stored-only, relay-only, or direct")
	return cmd
}

func (a *app) runTransferCreate(ctx context.Context, paths []string, expiresIn, maxDownloads int64, recipientRef, recipientFingerprint, senderRef string, trustOnFirstUse, alsoLink, includeSecret bool, transportRequest adaptive.Request) error {
	client, err := a.client()
	if err != nil {
		return err
	}
	absolutePaths := make([]string, len(paths))
	for i, source := range paths {
		absolutePaths[i], err = filepath.Abs(filepath.Clean(source))
		if err != nil {
			return &codedError{code: "input", err: err, exit: ExitUsage}
		}
	}
	plan, err := sealed.InspectInputs(absolutePaths)
	if err != nil {
		return &codedError{code: "input", err: err, exit: ExitUsage}
	}
	secrets, err := sealed.NewSecrets()
	if err != nil {
		return &codedError{code: "encryption", err: err, exit: ExitGeneric}
	}
	var addressed *addressedUpload
	if recipientRef != "" {
		recipient, resolveErr := a.resolveRecipient(ctx, recipientRef, trustOnFirstUse, recipientFingerprint)
		if resolveErr != nil {
			return resolveErr
		}
		pub, parseErr := identitypkg.ParsePublicKey(recipient.EncryptionPublicKey, 32)
		if parseErr != nil {
			return parseErr
		}
		addressed = &addressedUpload{Recipient: recipient, AlsoLink: alsoLink}
		if senderRef != "" {
			store, storeErr := a.identityStore()
			if storeErr != nil {
				return storeErr
			}
			sender, loadErr := store.LoadIdentity(senderRef)
			if loadErr != nil {
				return loadErr
			}
			if err := ensureLocalIdentityServer(sender, client.BaseURL); err != nil {
				return err
			}
			addressed.Sender = &sender
		}
		_ = pub
	}
	partSize := sealed.DefaultChunkSize + 16
	partCount := int((plan.CiphertextBytes + partSize - 1) / partSize)
	var max *int64
	if maxDownloads > 0 {
		max = &maxDownloads
	}
	create := api.TransferCreateRequest{
		Protocol: sealed.Protocol, DeclaredPlaintextBytes: plan.PlaintextBytes,
		CiphertextBytes: plan.CiphertextBytes, FileCount: len(plan.Files),
		PartCount: partCount, PartSize: partSize, ExpiresIn: expiresIn,
		MaxDownloads: max, ClaimCapability: secrets.ClaimCapabilityString(),
	}
	selection := adaptive.Selection{RequestedMode: transportRequest.Mode, Transport: adaptive.TransportR2, Reason: "stored_requested"}
	if transportRequest.Mode == adaptive.ModeAdaptive {
		selection = adaptive.Selection{RequestedMode: adaptive.ModeAdaptive, Transport: adaptive.TransportR2, Fallback: true, Reason: "service_unavailable"}
		discovered, discoveryErr := client.GetTransportCapabilities(ctx)
		if discoveryErr != nil {
			if errors.Is(discoveryErr, context.Canceled) || errors.Is(discoveryErr, context.DeadlineExceeded) {
				return discoveryErr
			}
			var apiErr *api.Error
			if errors.As(discoveryErr, &apiErr) && (apiErr.Status == 404 || apiErr.Code == "bad_response") {
				selection.Reason = "service_unavailable"
			} else if transientHandoffFailure(discoveryErr) {
				selection.Reason = "discovery_failed"
			} else {
				return discoveryErr
			}
		} else {
			// No reviewed live byte driver ships in this client yet, so the local
			// capability set is empty and selection must remain the durable R2 path.
			selection = adaptive.Select(transportRequest, discovered.Value, nil)
			if adaptive.SupportsRequest(transportRequest, discovered.Value) {
				create.TransportMode = adaptive.ModeAdaptive
				create.DurabilityPolicy = adaptive.DurableFirstWire
			}
		}
	}
	createKey := newIdempotencyKey()
	res, err := client.CreateTransfer(ctx, create, createKey)
	if err != nil {
		return err
	}
	t := res.Value
	if t.ID == "" || t.UploadCapability == "" || t.RevokeCapability == "" || t.CreatedAt <= 0 || t.ExpiresAt <= t.CreatedAt {
		return &codedError{code: "bad_response", err: errors.New("create transfer response omitted identifiers, capabilities, or timestamps"), exit: ExitGeneric}
	}
	t.Protocol = sealed.Protocol
	t.DeclaredPlaintextBytes = plan.PlaintextBytes
	t.CiphertextBytes = plan.CiphertextBytes
	t.FileCount = len(plan.Files)
	t.PartCount = partCount
	t.PartSize = partSize
	t.MaxDownloads = max
	if t.TransportMode == "" {
		t.TransportMode = adaptive.ModeStored
	}
	if t.DurabilityPolicy == "" {
		t.DurabilityPolicy = "durable_first"
	}
	if t.SelectedTransport == "" {
		t.SelectedTransport = adaptive.TransportR2
	}
	intent, err := handoff.FromSealed(client.BaseURL, t.ID, secrets)
	if err != nil {
		return err
	}
	recipientToken, err := intent.Token()
	if err != nil {
		return err
	}
	if addressed != nil {
		pub, _ := identitypkg.ParsePublicKey(addressed.Recipient.EncryptionPublicKey, 32)
		wrapped, wrapErr := identitypkg.WrapMasterKey(pub, t.ID, addressed.Recipient.IdentityID, addressed.Recipient.EncryptionKeyID, secrets.MasterKey)
		if wrapErr != nil {
			return wrapErr
		}
		addressed.WrappedMasterKey = base64.RawURLEncoding.EncodeToString(wrapped)
	}
	ticket := transferTicket{
		Version: 1, TransferID: t.ID, ServerURL: client.BaseURL,
		UploadCapability: t.UploadCapability, RevokeCapability: t.RevokeCapability,
		RecipientToken: recipientToken, SourcePaths: absolutePaths,
		CreatedAt: t.CreatedAt, ExpiresAt: t.ExpiresAt,
		TransportMode: t.TransportMode, DurabilityPolicy: t.DurabilityPolicy, SelectedTransport: t.SelectedTransport,
	}
	if addressed != nil {
		ticket.RecipientIdentityID = addressed.Recipient.IdentityID
		ticket.RecipientKeyID = addressed.Recipient.EncryptionKeyID
		ticket.RecipientFingerprint = addressed.Recipient.Fingerprint
		ticket.WrappedMasterKey = addressed.WrappedMasterKey
		ticket.AlsoLink = alsoLink
		if addressed.Sender != nil {
			ticket.SenderIdentityID = addressed.Sender.IdentityID
			ticket.SenderSigningKeyID = addressed.Sender.SigningKeyID
		}
	}
	ticketPath, err := a.writeTransferTicket(ticket)
	if err != nil {
		_ = client.RevokeTransfer(ctx, t.ID, t.RevokeCapability, t.ID+"-ticket-failure")
		return err
	}
	complete, ticket, err := a.finishTransferUpload(ctx, client, t, plan, secrets, ticket, ticketPath, nil, addressed)
	if err != nil {
		return err
	}
	link := ""
	if addressed == nil || alsoLink {
		fragment := secrets.Fragment()
		link = client.BaseURL + "/t/" + url.PathEscape(t.ID) + "#" + fragment
	}
	if a.jsonOut {
		output := map[string]any{
			"transfer":    complete.Value,
			"ticket_file": ticketPath, "manifest_sha256": ticket.ManifestSHA256, "ciphertext_sha256": ticket.CiphertextSHA256,
			"transport": selection.Transport, "transport_fallback": selection.Fallback, "transport_reason": selection.Reason,
		}
		if includeSecret && link != "" {
			output["link"] = link
			output["token"] = recipientToken
		}
		return a.printJSONValue(output)
	}
	fmt.Fprintf(a.stdout, "uploaded %d files (%s), encrypted locally\n", len(plan.Files), fmtBytes(plan.PlaintextBytes))
	if transportRequest.Mode == adaptive.ModeAdaptive {
		fmt.Fprintf(a.stdout, "transport adaptive → R2 (%s); durably stored\n", strings.ReplaceAll(selection.Reason, "_", " "))
	} else {
		fmt.Fprintln(a.stdout, "transport R2; durably stored")
	}
	if addressed != nil {
		fmt.Fprintf(a.stdout, "to      %s (%s), key %s\n", addressed.Recipient.Alias, addressed.Recipient.IdentityID, addressed.Recipient.EncryptionKeyID)
	}
	if link != "" {
		fmt.Fprintf(a.stdout, "link    %s\n", link)
	}
	fmt.Fprintf(a.stdout, "expires %s\n", fmtTime(t.ExpiresAt))
	fmt.Fprintf(a.stdout, "ticket  %s\n", ticketPath)
	fmt.Fprintln(a.stdout, "receipt pending")
	return nil
}

func (a *app) finishTransferUpload(ctx context.Context, client *api.Client, t api.Transfer, plan sealed.PlannedBundle, secrets sealed.Secrets, ticket transferTicket, ticketPath string, uploaded []api.TransferPart, addressed *addressedUpload) (api.Result[api.Transfer], transferTicket, error) {
	var empty api.Result[api.Transfer]
	cipherFile, err := os.CreateTemp("", "aispace-sealed-ciphertext-*")
	if err != nil {
		return empty, ticket, &codedError{code: "io", err: err, exit: ExitGeneric}
	}
	cipherPath := cipherFile.Name()
	defer func() { _ = os.Remove(cipherPath) }()
	if err := cipherFile.Chmod(0o600); err != nil {
		_ = cipherFile.Close()
		return empty, ticket, err
	}
	cipherSHA, err := sealed.EncryptBundle(ctx, plan, secrets.MasterKey, t.ID, cipherFile, nil)
	if closeErr := cipherFile.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return empty, ticket, &codedError{code: "encryption", err: err, exit: ExitGeneric}
	}
	ticket.CiphertextSHA256 = cipherSHA

	partSize := t.PartSize
	partCount := t.PartCount
	existing := make(map[int]api.TransferPart, len(uploaded))
	for _, part := range uploaded {
		existing[part.PartNumber] = part
	}
	parts := make([]api.TransferPart, 0, partCount)
	for number := 1; number <= partCount; number++ {
		offset := int64(number-1) * partSize
		size := min(partSize, plan.CiphertextBytes-offset)
		part, partSHA, err := readPart(cipherPath, offset, size)
		if err != nil {
			return empty, ticket, err
		}
		if previous, ok := existing[number]; ok && previous.SizeBytes == size && strings.EqualFold(previous.SHA256, partSHA) && previous.ETag != "" {
			parts = append(parts, previous)
			if !a.jsonOut {
				fmt.Fprintf(a.stderr, "kept verified part %d/%d\n", number, partCount)
			}
			continue
		}
		partRes, err := client.UploadTransferPart(ctx, t.ID, number, bytes.NewReader(part), size, partSHA, ticket.UploadCapability, fmt.Sprintf("%s-part-%d-%s", t.ID, number, partSHA[:16]))
		if err != nil {
			return empty, ticket, err
		}
		parts = append(parts, partRes.Value)
		if !a.jsonOut {
			fmt.Fprintf(a.stderr, "uploaded part %d/%d\n", number, partCount)
		}
	}

	manifest := sealed.ManifestFor(plan, t.ID, t.CreatedAt, t.ExpiresAt, cipherSHA)
	if addressed != nil {
		manifest.Recipient = &sealed.ManifestRecipient{IdentityID: addressed.Recipient.IdentityID, EncryptionKeyID: addressed.Recipient.EncryptionKeyID}
		var senderID, senderKeyID *string
		if addressed.Sender != nil {
			manifest.Sender = &sealed.ManifestSender{IdentityID: addressed.Sender.IdentityID, SigningKeyID: addressed.Sender.SigningKeyID}
			value, key := addressed.Sender.IdentityID, addressed.Sender.SigningKeyID
			senderID, senderKeyID = &value, &key
		}
		manifest.Delivery = &sealed.ManifestDelivery{
			Mode: "addressed", AlsoLink: addressed.AlsoLink, MaxDownloads: t.MaxDownloads,
			RecipientIdentityID: addressed.Recipient.IdentityID, RecipientKeyID: addressed.Recipient.EncryptionKeyID,
			SenderIdentityID: senderID, SenderSigningKeyID: senderKeyID,
			CreatedAt: t.CreatedAt, ExpiresAt: t.ExpiresAt, FileCount: len(plan.Files), DeclaredPlaintextBytes: plan.PlaintextBytes,
		}
		if addressed.Sender != nil {
			unsigned, signErr := sealed.CanonicalUnsignedManifest(manifest)
			if signErr != nil {
				return empty, ticket, signErr
			}
			private, signErr := addressed.Sender.SigningPrivate()
			if signErr != nil {
				return empty, ticket, signErr
			}
			signature, signErr := identitypkg.SignCanonical(private, identitypkg.ManifestSignatureDomain, unsigned)
			if signErr != nil {
				return empty, ticket, signErr
			}
			manifest.Signature = &sealed.ManifestSignature{Algorithm: identitypkg.SigningAlgorithm, Value: base64.RawURLEncoding.EncodeToString(signature)}
		}
	}
	canonical, err := sealed.CanonicalManifest(manifest)
	if err != nil {
		return empty, ticket, err
	}
	envelope, err := sealed.EncryptManifest(secrets.MasterKey, t.ID, canonical)
	if err != nil {
		return empty, ticket, err
	}
	manifestSHA := sealed.SHA256Hex(envelope)
	ticket.ManifestSHA256 = manifestSHA
	if err := a.replaceTransferTicket(ticketPath, ticket); err != nil {
		return empty, ticket, err
	}
	if err := client.UploadTransferManifest(ctx, t.ID, bytes.NewReader(envelope), int64(len(envelope)), manifestSHA, ticket.UploadCapability, t.ID+"-manifest-"+manifestSHA[:16]); err != nil {
		return empty, ticket, err
	}
	if addressed != nil {
		var senderID, senderKeyID *string
		if addressed.Sender != nil {
			value, key := addressed.Sender.IdentityID, addressed.Sender.SigningKeyID
			senderID = &value
			senderKeyID = &key
		}
		if _, err := client.PrepareDelivery(ctx, t.ID, api.PrepareDeliveryRequest{RecipientIdentityID: addressed.Recipient.IdentityID, RecipientKeyID: addressed.Recipient.EncryptionKeyID, WrappedMasterKey: addressed.WrappedMasterKey, SenderIdentityID: senderID, SenderSigningKeyID: senderKeyID, ManifestSHA256: manifestSHA, AlsoLink: addressed.AlsoLink}, t.ID+"-delivery"); err != nil {
			return empty, ticket, err
		}
	}
	complete, err := client.CompleteTransfer(ctx, t.ID, manifestSHA, cipherSHA, ticket.UploadCapability, t.ID+"-complete")
	if err != nil {
		return empty, ticket, err
	}
	return complete, ticket, nil
}

func (a *app) transferResumeCmd() *cobra.Command {
	var ticketPath string
	cmd := &cobra.Command{
		Use:   "resume <transfer-id> [--ticket PATH]",
		Short: "Resume a sealed upload without replacing verified parts",
		Args:  exactArgs(1, "<transfer-id>"),
		RunE: func(cmd *cobra.Command, args []string) error {
			var err error
			if ticketPath == "" {
				ticketPath, err = a.transferTicketPath(args[0])
				if err != nil {
					return err
				}
			}
			ticket, err := readTransferTicket(ticketPath)
			if err != nil {
				return err
			}
			if ticket.TransferID != args[0] || len(ticket.SourcePaths) == 0 || ticket.UploadCapability == "" {
				return usagef("ticket cannot resume transfer %s", args[0])
			}
			client, err := a.clientForTransferTicket(ticket)
			if err != nil {
				return err
			}
			intent, err := handoff.Parse(ticket.RecipientToken, ticket.ServerURL)
			if err != nil || intent.TransferID != args[0] || len(intent.MasterKey) != 32 || len(intent.ClaimCapability) != 32 {
				return &codedError{code: "ticket", err: errors.New("ticket contains an invalid recipient token"), exit: ExitGeneric}
			}
			var secrets sealed.Secrets
			copy(secrets.MasterKey[:], intent.MasterKey)
			copy(secrets.ClaimCapability[:], intent.ClaimCapability)
			status, err := client.GetTransfer(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			if status.Value.State != "uploading" {
				return usagef("transfer %s is %s, not uploading", args[0], status.Value.State)
			}
			plan, err := sealed.InspectInputs(ticket.SourcePaths)
			if err != nil {
				return err
			}
			t := status.Value
			if plan.PlaintextBytes != t.DeclaredPlaintextBytes || plan.CiphertextBytes != t.CiphertextBytes || len(plan.Files) != t.FileCount {
				return &codedError{code: "input_changed", err: errors.New("source files no longer match the reserved transfer declaration"), exit: ExitGeneric}
			}
			var addressed *addressedUpload
			if ticket.RecipientIdentityID != "" {
				recipient := identitypkg.Recipient{Alias: ticket.RecipientIdentityID, IdentityID: ticket.RecipientIdentityID, EncryptionKeyID: ticket.RecipientKeyID, Fingerprint: ticket.RecipientFingerprint, TrustState: "pinned"}
				addressed = &addressedUpload{Recipient: recipient, WrappedMasterKey: ticket.WrappedMasterKey, AlsoLink: ticket.AlsoLink}
				if ticket.SenderIdentityID != "" {
					store, storeErr := a.identityStore()
					if storeErr != nil {
						return storeErr
					}
					sender, loadErr := store.LoadIdentity(ticket.SenderIdentityID)
					if loadErr != nil {
						return loadErr
					}
					if serverErr := ensureLocalIdentityServer(sender, client.BaseURL); serverErr != nil {
						return serverErr
					}
					if ticket.SenderSigningKeyID != "" && sender.SigningKeyID != ticket.SenderSigningKeyID {
						private, findErr := sender.SigningPrivateFor(ticket.SenderSigningKeyID)
						if findErr != nil {
							return findErr
						}
						for _, key := range sender.SigningKeys {
							if key.KeyID == ticket.SenderSigningKeyID {
								sender.SigningKeyID = key.KeyID
								sender.SigningPrivateKey = base64.RawURLEncoding.EncodeToString(private)
								sender.SigningPublicKey = key.PublicKey
								break
							}
						}
					}
					addressed.Sender = &sender
				}
			}
			complete, ticket, err := a.finishTransferUpload(cmd.Context(), client, t, plan, secrets, ticket, ticketPath, t.UploadedParts, addressed)
			if err != nil {
				return err
			}
			if a.jsonOut {
				return a.printJSONValue(map[string]any{"transfer": complete.Value, "ticket_file": ticketPath, "manifest_sha256": ticket.ManifestSHA256, "ciphertext_sha256": ticket.CiphertextSHA256})
			}
			fmt.Fprintf(a.stdout, "resumed %s: %d files, encrypted and finalized\n", t.ID, len(plan.Files))
			return nil
		},
	}
	cmd.Flags().StringVar(&ticketPath, "ticket", "", "owner ticket path (default: saved ticket for this transfer)")
	return cmd
}

func readPart(path string, offset, size int64) ([]byte, string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, "", err
	}
	defer f.Close()
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return nil, "", err
	}
	b := make([]byte, size)
	if _, err := io.ReadFull(f, b); err != nil {
		return nil, "", err
	}
	sum := sha256.Sum256(b)
	return b, hex.EncodeToString(sum[:]), nil
}

func (a *app) transferReceiveCmd() *cobra.Command {
	var tokenFile, outputDir string
	var yes, overwrite bool
	cmd := &cobra.Command{
		Use:   "receive [link-or-token] [--token-file PATH] [--output DIR] [--yes]",
		Short: "Inspect, claim, decrypt, and verify a sealed transfer",
		Long: "Reads a sealed link or token from a mode-0600 file, AISPACE_TRANSFER_TOKEN, stdin,\n" +
			"or (for convenience) one argument. Prefer a file, environment variable, or the prompt:\n" +
			"process arguments may be visible to other local users. The master key is never sent.",
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) > 1 {
				return usagef("expected at most one link or token")
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			ref, err := a.readTransferReference(args, tokenFile)
			if err != nil {
				return err
			}
			return a.runTransferReceive(cmd.Context(), ref, outputDir, yes, overwrite)
		},
	}
	cmd.Flags().StringVar(&tokenFile, "token-file", "", "read the link or token from a mode-0600 file")
	cmd.Flags().StringVarP(&outputDir, "output", "o", ".", "destination directory")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "accept after manifest inspection without prompting")
	cmd.Flags().BoolVar(&overwrite, "overwrite", false, "replace existing destination files only after verification")
	return cmd
}

func (a *app) readTransferReference(args []string, tokenFile string) (string, error) {
	if tokenFile != "" {
		if len(args) != 0 || os.Getenv(transferTokenEnv) != "" {
			return "", usagef("use only one of an argument, --token-file, or %s", transferTokenEnv)
		}
		st, err := os.Stat(tokenFile)
		if err != nil {
			return "", err
		}
		if st.Mode().Perm()&0o077 != 0 {
			return "", usagef("token file %s permissions are %04o; expected 0600", tokenFile, st.Mode().Perm())
		}
		b, err := os.ReadFile(tokenFile)
		return strings.TrimSpace(string(b)), err
	}
	if env := os.Getenv(transferTokenEnv); env != "" {
		if len(args) != 0 {
			return "", usagef("use only one of an argument or %s", transferTokenEnv)
		}
		return strings.TrimSpace(env), nil
	}
	if len(args) == 1 {
		return args[0], nil
	}
	if !a.jsonOut {
		fmt.Fprint(a.stderr, "Paste link or token: ")
	}
	var line string
	var err error
	if file, ok := a.stdin.(*os.File); ok && term.IsTerminal(int(file.Fd())) {
		var secret []byte
		secret, err = term.ReadPassword(int(file.Fd()))
		line = string(secret)
		if !a.jsonOut {
			fmt.Fprintln(a.stderr)
		}
	} else {
		line, err = readOneLine(a.stdin)
	}
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	if strings.TrimSpace(line) == "" {
		return "", usagef("no transfer link or token supplied")
	}
	return strings.TrimSpace(line), nil
}

// readOneLine avoids read-ahead so a subsequent confirmation prompt can read
// the next line from a pipe as well as from a terminal.
func readOneLine(r io.Reader) (string, error) {
	var buf bytes.Buffer
	var one [1]byte
	for {
		n, err := r.Read(one[:])
		if n > 0 {
			if one[0] == '\n' {
				return buf.String(), nil
			}
			_ = buf.WriteByte(one[0])
		}
		if err != nil {
			return buf.String(), err
		}
	}
}

func (a *app) runTransferReceive(ctx context.Context, reference, outputDir string, yes, overwrite bool) error {
	cfg, err := a.resolve()
	if err != nil {
		return err
	}
	intent, err := handoff.Parse(reference, cfg.URL)
	if err != nil {
		return usagef("invalid transfer reference: %v", err)
	}
	if intent.Mode != handoff.ModeSealedLink || len(intent.MasterKey) != 32 || len(intent.ClaimCapability) != 32 {
		return usagef("transfer reference is not a sealed-link handoff")
	}
	transferID := intent.TransferID
	var secrets sealed.Secrets
	copy(secrets.MasterKey[:], intent.MasterKey)
	copy(secrets.ClaimCapability[:], intent.ClaimCapability)
	client := api.New(intent.ServiceOrigin, "", a.userAgent())
	if inactivityTimeout > 0 {
		client.InactivityTimeout = inactivityTimeout
	}
	manifestResp, err := client.GetTransferManifest(ctx, transferID, secrets.ClaimCapabilityString())
	if err != nil {
		return err
	}
	envelope, readErr := io.ReadAll(io.LimitReader(manifestResp.Body, sealed.MaxManifestEnvelopeBytes+1))
	_ = manifestResp.Body.Close()
	if readErr != nil {
		return readErr
	}
	manifestSHA := sealed.SHA256Hex(envelope)
	if want := strings.ToLower(manifestResp.Header.Get("X-Manifest-SHA256")); want != "" && want != manifestSHA {
		return &codedError{code: "checksum_mismatch", err: errors.New("encrypted manifest digest does not match response header"), exit: ExitGeneric}
	}
	plain, err := sealed.DecryptManifest(secrets.MasterKey, transferID, envelope)
	if err != nil {
		return &codedError{code: "authentication", err: err, exit: ExitGeneric}
	}
	manifest, err := sealed.DecodeManifest(plain)
	if err != nil {
		return &codedError{code: "bad_manifest", err: err, exit: ExitGeneric}
	}
	if manifest.TransferID != transferID {
		return &codedError{code: "bad_manifest", err: errors.New("manifest transfer ID does not match link"), exit: ExitGeneric}
	}
	if !a.jsonOut {
		fmt.Fprintf(a.stdout, "sealed transfer from Unknown sender; expires %s\n", fmtTime(manifest.ExpiresAt))
		for _, f := range manifest.Files {
			fmt.Fprintf(a.stdout, "  %s  %s\n", fmtBytes(f.SizeBytes), f.Path)
		}
	}
	if !yes {
		if a.jsonOut {
			return usagef("--json receive requires --yes because prompts are disabled")
		}
		fmt.Fprint(a.stderr, "Receive and save these files? [y/N] ")
		line, _ := readOneLine(a.stdin)
		answer := strings.ToLower(strings.TrimSpace(line))
		if answer != "y" && answer != "yes" {
			return &codedError{code: "declined", err: errors.New("transfer declined before claiming"), exit: ExitGeneric}
		}
	}
	if err := sealed.PreflightReceive(manifest, sealed.ReceiveOptions{OutputDir: outputDir, Overwrite: overwrite}); err != nil {
		return &codedError{code: "output", err: err, exit: ExitUsage}
	}
	claim, err := client.ClaimTransfer(ctx, transferID, secrets.ClaimCapabilityString(), newIdempotencyKey())
	if err != nil {
		return err
	}
	if claim.Value.ID == "" || claim.Value.Token == "" {
		return &codedError{code: "bad_response", err: errors.New("claim response omitted claim ID or token"), exit: ExitGeneric}
	}
	committed := false
	defer func() {
		if !committed {
			releaseCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			_ = client.ReleaseTransferClaim(releaseCtx, transferID, claim.Value.ID, claim.Value.Token)
		}
	}()
	open := func(openCtx context.Context, offset int64) (io.ReadCloser, error) {
		rangeHeader := ""
		if offset > 0 {
			rangeHeader = "bytes=" + strconv.FormatInt(offset, 10) + "-"
		}
		resp, err := client.DownloadTransferContent(openCtx, transferID, claim.Value.Token, rangeHeader)
		if err != nil {
			return nil, err
		}
		return resp.Body, nil
	}
	lastRenew := int64(0)
	lastRenewAt := time.Now()
	totalCiphertext := totalCiphertextBytes(manifest)
	err = sealed.ReceiveBundle(ctx, manifest, secrets.MasterKey, open, sealed.ReceiveOptions{
		OutputDir: outputDir, Overwrite: overwrite, Retries: 2,
		Progress: func(received int64) error {
			if received-lastRenew < 32<<20 && time.Since(lastRenewAt) < 5*time.Minute && received < totalCiphertext {
				return nil
			}
			_, err := client.RenewTransferClaim(ctx, transferID, claim.Value.ID, claim.Value.Token, received)
			if err == nil {
				lastRenew = received
				lastRenewAt = time.Now()
			}
			return err
		},
	})
	if err != nil {
		return &codedError{code: "receive", err: err, exit: ExitGeneric}
	}
	receipt, err := client.CommitTransferClaim(ctx, transferID, claim.Value.ID, claim.Value.Token, manifestSHA, manifest.CiphertextSHA256, newIdempotencyKey())
	if err != nil {
		return err
	}
	committed = true
	if a.jsonOut {
		return a.printJSONValue(map[string]any{"manifest": manifest, "receipt": receipt.Value, "output": outputDir})
	}
	fmt.Fprintf(a.stdout, "Verified download: %d file(s) saved to %s\n", len(manifest.Files), outputDir)
	return nil
}

func totalCiphertextBytes(m sealed.Manifest) int64 {
	var total int64
	for _, f := range m.Files {
		for _, ch := range f.Chunks {
			total += ch.PlaintextBytes + 16
		}
	}
	return total
}

func (a *app) transferBaseURL(reference string) (string, error) {
	if u, err := url.Parse(strings.TrimSpace(reference)); err == nil && u.Host != "" {
		origin := u.Scheme + "://" + u.Host
		if err := validateServerURL(origin); err != nil {
			return "", err
		}
		return origin, nil
	}
	cfg, err := a.resolve()
	if err != nil {
		return "", err
	}
	if err := validateServerURL(cfg.URL); err != nil {
		return "", err
	}
	return cfg.URL, nil
}

func (a *app) transferStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status <transfer-id>",
		Short: "Show owner-visible transfer state and uploaded parts",
		Args:  exactArgs(1, "<transfer-id>"),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := a.client()
			if err != nil {
				return err
			}
			res, err := client.GetTransfer(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			if a.jsonOut {
				a.printJSON(res.Raw)
				return nil
			}
			transport := res.Value.SelectedTransport
			if transport == "" {
				transport = adaptive.TransportR2
			}
			fmt.Fprintf(a.stdout, "%s %s %d/%d parts transport %s expires %s\n", res.Value.ID, res.Value.State, len(res.Value.UploadedParts), res.Value.PartCount, transport, fmtTime(res.Value.ExpiresAt))
			return nil
		},
	}
}

func (a *app) transferRevokeCmd() *cobra.Command {
	var ticketPath string
	cmd := &cobra.Command{
		Use:   "revoke <transfer-id> [--ticket PATH]",
		Short: "Abort or revoke a transfer using its mode-0600 owner ticket",
		Args:  exactArgs(1, "<transfer-id>"),
		RunE: func(cmd *cobra.Command, args []string) error {
			var err error
			if ticketPath == "" {
				ticketPath, err = a.transferTicketPath(args[0])
				if err != nil {
					return err
				}
			}
			ticket, err := readTransferTicket(ticketPath)
			if err != nil {
				return err
			}
			if ticket.TransferID != args[0] {
				return usagef("ticket belongs to transfer %s, not %s", ticket.TransferID, args[0])
			}
			client, err := a.clientForTransferTicket(ticket)
			if err != nil {
				return err
			}
			if err := client.RevokeTransfer(cmd.Context(), args[0], ticket.RevokeCapability, args[0]+"-revoke"); err != nil {
				return err
			}
			if !a.jsonOut {
				fmt.Fprintf(a.stdout, "revoked %s\n", args[0])
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&ticketPath, "ticket", "", "owner ticket path (default: saved ticket for this transfer)")
	return cmd
}

func (a *app) clientForTransferTicket(ticket transferTicket) (*api.Client, error) {
	ticketOrigin, err := handoff.CanonicalOrigin(ticket.ServerURL)
	if err != nil {
		return nil, &codedError{code: "ticket", err: fmt.Errorf("ticket server URL: %w", err), exit: ExitUsage}
	}
	cfg, err := a.resolve()
	if err != nil {
		return nil, err
	}
	configuredOrigin, err := handoff.CanonicalOrigin(cfg.URL)
	if err != nil {
		return nil, &codedError{code: "config", err: fmt.Errorf("configured server URL: %w", err), exit: ExitUsage}
	}
	if ticketOrigin != configuredOrigin {
		return nil, &codedError{code: "ticket_origin", err: errors.New("ticket server does not match the configured service"), exit: ExitUsage}
	}
	client, err := a.client()
	if err != nil {
		return nil, err
	}
	client.BaseURL = ticketOrigin
	return client, nil
}

var outputIsTerminal = func(writer io.Writer) bool {
	file, ok := writer.(*os.File)
	return ok && term.IsTerminal(int(file.Fd()))
}

func (a *app) transferTicketPath(id string) (string, error) {
	cfg, err := a.resolve()
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(cfg.Path), "transfers", id+".json"), nil
}

func (a *app) writeTransferTicket(ticket transferTicket) (string, error) {
	path, err := a.transferTicketPath(ticket.TransferID)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", err
	}
	b, _ := json.MarshalIndent(ticket, "", "  ")
	if err := writeSecretFile(path, string(append(b, '\n'))); err != nil {
		return "", err
	}
	return path, nil
}

func (a *app) replaceTransferTicket(path string, ticket transferTicket) error {
	b, _ := json.MarshalIndent(ticket, "", "  ")
	tmp := path + ".new"
	if err := writeSecretFile(tmp, string(append(b, '\n'))); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

func readTransferTicket(path string) (transferTicket, error) {
	var ticket transferTicket
	st, err := os.Stat(path)
	if err != nil {
		return ticket, err
	}
	if st.Mode().Perm()&0o077 != 0 {
		return ticket, usagef("ticket file %s permissions are %04o; expected 0600", path, st.Mode().Perm())
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return ticket, err
	}
	if err := json.Unmarshal(b, &ticket); err != nil {
		return ticket, err
	}
	if ticket.Version != 1 || ticket.TransferID == "" || ticket.RevokeCapability == "" {
		return ticket, errors.New("invalid sealed transfer owner ticket")
	}
	return ticket, nil
}

func newIdempotencyKey() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return hex.EncodeToString(b[:])
}
