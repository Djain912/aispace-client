package cmd

import (
	"context"
	"crypto/ed25519"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/aispace-sh/aispace-client/internal/api"
	identitypkg "github.com/aispace-sh/aispace-client/internal/identity"
	"github.com/aispace-sh/aispace-client/internal/sealed"
)

type pendingInboxClaim struct {
	Version             int    `json:"version"`
	ServerURL           string `json:"server_url"`
	DeliveryID          string `json:"delivery_id"`
	RecipientIdentityID string `json:"recipient_identity_id"`
	IdempotencyKey      string `json:"idempotency_key"`
}

type pendingInboxReceiptSubmission struct {
	Event          api.DeliveryReceiptEvent `json:"event"`
	Reject         bool                     `json:"reject"`
	IdempotencyKey string                   `json:"idempotency_key"`
}

type pendingInboxReceiptSequence struct {
	Version     int                             `json:"version"`
	ServerURL   string                          `json:"server_url"`
	DeliveryID  string                          `json:"delivery_id"`
	Flow        string                          `json:"flow"`
	Next        int                             `json:"next"`
	Submissions []pendingInboxReceiptSubmission `json:"submissions"`
	Final       *api.DeliveryReceiptEnvelope    `json:"final,omitempty"`
	Completion  *pendingInboxReceiptCompletion  `json:"completion,omitempty"`
}

type pendingInboxReceiptCompletion struct {
	OutputDir string `json:"output_dir"`
	FileCount int    `json:"file_count"`
}

func (a *app) inboxCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "inbox", Short: "Receive deliveries addressed to local identities", Args: noArgs}
	cmd.AddCommand(a.inboxListCmd(), a.inboxReceiveCmd(), a.inboxRejectCmd(), a.inboxProcessedCmd())
	return cmd
}
func (a *app) inboxListCmd() *cobra.Command {
	var cursor string
	var limit int
	cmd := &cobra.Command{Use: "list", Short: "List pending and recent addressed deliveries", Args: noArgs, RunE: func(cmd *cobra.Command, args []string) error {
		client, err := a.client()
		if err != nil {
			return err
		}
		res, err := client.ListInbox(cmd.Context(), cursor, limit)
		if err != nil {
			return err
		}
		if a.jsonOut {
			a.printJSON(res.Raw)
			return nil
		}
		for _, d := range res.Value.Deliveries {
			sender := "unknown"
			if d.SenderIdentityID != nil {
				sender = *d.SenderIdentityID
			}
			fmt.Fprintf(a.stdout, "%s\t%s\tfrom %s\t%s\n", d.ID, d.State, sender, fmtTime(d.ExpiresAt))
		}
		return nil
	}}
	cmd.Flags().StringVar(&cursor, "cursor", "", "pagination cursor")
	cmd.Flags().IntVar(&limit, "limit", 0, "items to return (1-100)")
	return cmd
}

func signReceipt(local identitypkg.LocalIdentity, claim api.InboxClaim, kind, manifestSHA string) (api.DeliveryReceiptEvent, error) {
	private, err := local.SigningPrivate()
	if err != nil {
		return api.DeliveryReceiptEvent{}, err
	}
	receipt := api.SignedDeliveryReceipt{Protocol: "aispace-delivery-receipt-v1", ReceiptID: newULID(time.Now().Unix()), DeliveryID: claim.Delivery.ID, TransferID: claim.Delivery.TransferID, Type: kind, RecipientIdentityID: local.IdentityID, SigningKeyID: local.SigningKeyID, ClaimID: claim.ClaimID, ClaimNonce: claim.ClaimNonce, ManifestSHA256: manifestSHA, EventAt: time.Now().Unix()}
	canonical, err := identitypkg.CanonicalJSON(receipt)
	if err != nil {
		return api.DeliveryReceiptEvent{}, err
	}
	sig, err := identitypkg.SignCanonical(private, identitypkg.ReceiptSignatureDomain, canonical)
	if err != nil {
		return api.DeliveryReceiptEvent{}, err
	}
	return api.DeliveryReceiptEvent{Receipt: receipt, Signature: base64.RawURLEncoding.EncodeToString(sig)}, nil
}

func (a *app) claimInbox(ctx context.Context, client *api.Client, deliveryID, identityRef string) (api.InboxClaim, identitypkg.LocalIdentity, error) {
	store, err := a.identityStore()
	if err != nil {
		return api.InboxClaim{}, identitypkg.LocalIdentity{}, err
	}
	var pending pendingInboxClaim
	pendingErr := store.LoadPending("inbox-claim", deliveryID, &pending)
	if pendingErr != nil && !errors.Is(pendingErr, os.ErrNotExist) {
		return api.InboxClaim{}, identitypkg.LocalIdentity{}, pendingErr
	}
	hasPending := pendingErr == nil
	if hasPending && identityRef == "" {
		identityRef = pending.RecipientIdentityID
	}
	var local identitypkg.LocalIdentity
	if identityRef != "" {
		local, err = store.LoadIdentity(identityRef)
		if err != nil {
			return api.InboxClaim{}, local, err
		}
	} else {
		items, listErr := client.ListInbox(ctx, "", 100)
		if listErr != nil {
			return api.InboxClaim{}, local, listErr
		}
		var identityID string
		for _, d := range items.Value.Deliveries {
			if d.ID == deliveryID {
				identityID = d.RecipientIdentityID
				break
			}
		}
		if identityID == "" {
			return api.InboxClaim{}, local, errors.New("delivery is not in the current inbox page; provide --identity")
		}
		local, err = store.LoadIdentity(identityID)
		if err != nil {
			return api.InboxClaim{}, local, err
		}
	}
	if err := ensureLocalIdentityServer(local, client.BaseURL); err != nil {
		return api.InboxClaim{}, local, err
	}
	if hasPending {
		if pending.Version != 1 || pending.ServerURL != client.BaseURL || pending.DeliveryID != deliveryID || pending.RecipientIdentityID != local.IdentityID || pending.IdempotencyKey == "" {
			return api.InboxClaim{}, local, errors.New("stored pending inbox claim is invalid or belongs to another identity/server")
		}
	} else {
		pending = pendingInboxClaim{Version: 1, ServerURL: client.BaseURL, DeliveryID: deliveryID, RecipientIdentityID: local.IdentityID, IdempotencyKey: newIdempotencyKey()}
		if _, err := store.SavePending("inbox-claim", deliveryID, pending); err != nil {
			return api.InboxClaim{}, local, fmt.Errorf("cannot persist recoverable inbox claim request: %w", err)
		}
	}
	for attempt := 0; attempt < 2; attempt++ {
		claim, claimErr := client.ClaimInboxDelivery(ctx, deliveryID, local.IdentityID, pending.IdempotencyKey)
		if claimErr != nil {
			return api.InboxClaim{}, local, fmt.Errorf("claim response was not recovered; exact replay state was retained: %w", claimErr)
		}
		if claim.Value.ClaimID == "" || claim.Value.ClaimNonce == "" {
			return api.InboxClaim{}, local, errors.New("claim response omitted claim ID or nonce")
		}
		if claim.Value.Delivery.ID != deliveryID || claim.Value.Delivery.RecipientIdentityID != local.IdentityID || claim.Value.Transfer.ID == "" || claim.Value.Transfer.ID != claim.Value.Delivery.TransferID {
			return api.InboxClaim{}, local, errors.New("claim response does not match the requested delivery and local identity")
		}
		if claim.Value.LeaseExpiresAt > time.Now().Unix() {
			return claim.Value, local, nil
		}
		if err := store.RemovePending("inbox-claim", deliveryID); err != nil {
			return api.InboxClaim{}, local, err
		}
		pending.IdempotencyKey = newIdempotencyKey()
		if _, err := store.SavePending("inbox-claim", deliveryID, pending); err != nil {
			return api.InboxClaim{}, local, err
		}
	}
	return api.InboxClaim{}, local, errors.New("service replayed an expired inbox claim")
}

func (a *app) inboxReceiveCmd() *cobra.Command {
	var identityRef, outputDir string
	var yes, overwrite, allowUnknownSender bool
	cmd := &cobra.Command{Use: "receive <delivery-id>", Short: "Claim, decrypt, verify, and save an addressed delivery", Args: exactArgs(1, "delivery ID"), RunE: func(cmd *cobra.Command, args []string) error {
		client, err := a.client()
		if err != nil {
			return err
		}
		claim, local, err := a.claimInbox(cmd.Context(), client, args[0], identityRef)
		if err != nil {
			return err
		}
		store, storeErr := a.identityStore()
		if storeErr != nil {
			return storeErr
		}
		if recovered, recoverErr := a.recoverCompletedInboxReceive(cmd.Context(), client, store, claim, local, outputDir); recovered || recoverErr != nil {
			return recoverErr
		}
		private, err := local.EncryptionPrivateFor(claim.Delivery.RecipientKeyID)
		if err != nil {
			return err
		}
		wrapped, err := base64.RawURLEncoding.DecodeString(claim.Delivery.WrappedMasterKey)
		if err != nil {
			return errors.New("delivery contains an invalid HPKE envelope")
		}
		master, err := identitypkg.UnwrapMasterKey(private, claim.Transfer.ID, local.IdentityID, claim.Delivery.RecipientKeyID, wrapped)
		if err != nil {
			return err
		}
		resp, err := client.InboxManifest(cmd.Context(), claim.Delivery.ID, claim.ClaimID, claim.ClaimNonce)
		if err != nil {
			return err
		}
		envelope, readErr := io.ReadAll(io.LimitReader(resp.Body, sealed.MaxManifestEnvelopeBytes+1))
		_ = resp.Body.Close()
		if readErr != nil {
			return readErr
		}
		manifestSHA := sealed.SHA256Hex(envelope)
		if err := validateInboxManifestEnvelope(claim, manifestSHA, int64(len(envelope))); err != nil {
			return err
		}
		plain, err := sealed.DecryptManifest(master, claim.Transfer.ID, envelope)
		if err != nil {
			return err
		}
		manifest, err := sealed.DecodeManifest(plain)
		if err != nil {
			return err
		}
		if err := validateInboxManifestBinding(manifest, claim, local.IdentityID); err != nil {
			return err
		}
		senderStatus, err := a.verifyManifestSenderWithClaim(cmd.Context(), client, manifest, claim.SenderSigningKey, claim.SenderIdentityState)
		if err != nil {
			return err
		}
		if yes && !allowUnknownSender && !strings.HasSuffix(senderStatus, " (verified sender)") {
			return usagef("automated receive from an unknown or unpinned sender requires --allow-unknown-sender")
		}
		if !a.jsonOut {
			fmt.Fprintf(a.stdout, "sealed delivery from %s; expires %s\n", senderStatus, fmtTime(manifest.ExpiresAt))
			for _, f := range manifest.Files {
				fmt.Fprintf(a.stdout, "  %s  %s\n", fmtBytes(f.SizeBytes), f.Path)
			}
		}
		if !yes {
			if a.jsonOut {
				return usagef("--json receive requires --yes")
			}
			fmt.Fprint(a.stderr, "Receive and save these files? [y/N] ")
			line, _ := readOneLine(a.stdin)
			if answer := strings.ToLower(strings.TrimSpace(line)); answer != "y" && answer != "yes" {
				return errors.New("delivery left pending")
			}
		}
		if err := sealed.PreflightReceive(manifest, sealed.ReceiveOptions{OutputDir: outputDir, Overwrite: overwrite}); err != nil {
			return err
		}
		open := func(openCtx context.Context, offset int64) (io.ReadCloser, error) {
			rangeHeader := ""
			if offset > 0 {
				rangeHeader = "bytes=" + strconv.FormatInt(offset, 10) + "-"
			}
			r, e := client.InboxContent(openCtx, claim.Delivery.ID, claim.ClaimID, claim.ClaimNonce, rangeHeader)
			if e != nil {
				return nil, e
			}
			return r.Body, nil
		}
		last := int64(0)
		lastAt := time.Now()
		total := totalCiphertextBytes(manifest)
		err = sealed.ReceiveBundle(cmd.Context(), manifest, master, open, sealed.ReceiveOptions{OutputDir: outputDir, Overwrite: overwrite, Retries: 2, Progress: func(received int64) error {
			if received-last < 32<<20 && time.Since(lastAt) < 5*time.Minute && received < total {
				return nil
			}
			_, e := client.RenewInboxClaim(cmd.Context(), claim.Delivery.ID, claim.ClaimID, claim.ClaimNonce, received)
			if e == nil {
				last = received
				lastAt = time.Now()
			}
			return e
		}})
		if err != nil {
			return err
		}
		downloaded, err := signReceipt(local, claim, "downloaded", manifestSHA)
		if err != nil {
			return err
		}
		event, err := signReceipt(local, claim, "verified", manifestSHA)
		if err != nil {
			return err
		}
		absoluteOutput, err := filepath.Abs(outputDir)
		if err != nil {
			return err
		}
		completion := &pendingInboxReceiptCompletion{OutputDir: filepath.Clean(absoluteOutput), FileCount: len(manifest.Files)}
		receipt, err := submitInboxReceiptSequence(cmd.Context(), client, store, claim.Delivery.ID, "receive", []pendingInboxReceiptSubmission{{Event: downloaded, IdempotencyKey: newIdempotencyKey()}, {Event: event, IdempotencyKey: newIdempotencyKey()}}, true, completion)
		if err != nil {
			return err
		}
		contextPath, storeErr := store.SaveDeliveryContext(identitypkg.DeliveryContext{DeliveryID: claim.Delivery.ID, TransferID: claim.Delivery.TransferID, RecipientIdentityID: local.IdentityID, SigningKeyID: local.SigningKeyID, ClaimID: claim.ClaimID, ClaimNonce: claim.ClaimNonce, ManifestSHA256: manifestSHA, VerifiedAt: time.Now().Unix()})
		if storeErr != nil {
			return fmt.Errorf("verified receipt accepted but processing context could not be saved: %w", storeErr)
		}
		if storeErr := store.RemovePending("inbox-claim", claim.Delivery.ID); storeErr != nil {
			return fmt.Errorf("verified receipt accepted but pending claim state could not be removed: %w", storeErr)
		}
		if storeErr := store.RemovePending("receipt-receive", claim.Delivery.ID); storeErr != nil {
			return fmt.Errorf("verified receipt accepted but receipt replay state could not be removed: %w", storeErr)
		}
		if a.jsonOut {
			return a.printJSONValue(map[string]any{"manifest": manifest, "receipt": receipt.Value, "sender_trust": senderStatus, "receipt_context": contextPath})
		}
		fmt.Fprintf(a.stdout, "Verified delivery: %d file(s) saved to %s\n", len(manifest.Files), outputDir)
		return nil
	}}
	cmd.Flags().StringVar(&identityRef, "identity", "", "local recipient identity ID or handle")
	cmd.Flags().StringVarP(&outputDir, "output", "o", ".", "destination directory")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "accept after manifest inspection")
	cmd.Flags().BoolVar(&overwrite, "overwrite", false, "replace existing files after verification")
	cmd.Flags().BoolVar(&allowUnknownSender, "allow-unknown-sender", false, "explicitly allow automated receive from an unknown or unpinned sender")
	return cmd
}

func validateInboxManifestEnvelope(claim api.InboxClaim, manifestSHA string, manifestSize int64) error {
	if manifestSHA == "" || claim.Delivery.ManifestSHA256 != manifestSHA || claim.Transfer.ManifestSHA256 == nil || *claim.Transfer.ManifestSHA256 != manifestSHA {
		return errors.New("encrypted manifest digest does not match authenticated delivery and transfer metadata")
	}
	if claim.Transfer.ManifestSizeBytes == nil || *claim.Transfer.ManifestSizeBytes != manifestSize {
		return errors.New("encrypted manifest size does not match authenticated transfer metadata")
	}
	return nil
}

func validateInboxManifestBinding(manifest sealed.Manifest, claim api.InboxClaim, localIdentityID string) error {
	d := manifest.Delivery
	if d == nil || d.Mode != "addressed" {
		return errors.New("manifest lacks an addressed delivery policy binding")
	}
	if manifest.TransferID != claim.Transfer.ID || claim.Delivery.TransferID != claim.Transfer.ID {
		return errors.New("manifest transfer ID does not match the authenticated inbox transfer")
	}
	if manifest.CreatedAt != claim.Transfer.CreatedAt || manifest.ExpiresAt != claim.Transfer.ExpiresAt || d.CreatedAt != claim.Transfer.CreatedAt || d.ExpiresAt != claim.Transfer.ExpiresAt || claim.Delivery.ExpiresAt != claim.Transfer.ExpiresAt {
		return errors.New("manifest timestamps do not match authenticated transfer metadata")
	}
	if d.FileCount != len(manifest.Files) || d.FileCount != claim.Transfer.FileCount {
		return errors.New("manifest file count does not match authenticated transfer metadata")
	}
	var plaintextBytes int64
	for _, file := range manifest.Files {
		plaintextBytes += file.SizeBytes
	}
	if d.DeclaredPlaintextBytes != plaintextBytes || plaintextBytes != claim.Transfer.DeclaredPlaintextBytes {
		return errors.New("manifest plaintext byte total does not match authenticated transfer metadata")
	}
	if !equalOptionalInt64(d.MaxDownloads, claim.Transfer.MaxDownloads) || d.AlsoLink != claim.Delivery.AlsoLink || d.AlsoLink != claim.Transfer.AlsoLink {
		return errors.New("manifest delivery policy does not match authenticated server metadata")
	}
	if manifest.CiphertextSHA256 == "" || claim.Transfer.CiphertextSHA256 == nil || manifest.CiphertextSHA256 != *claim.Transfer.CiphertextSHA256 || totalCiphertextBytes(manifest) != claim.Transfer.CiphertextBytes {
		return errors.New("manifest ciphertext metadata does not match authenticated transfer metadata")
	}
	if manifest.Recipient == nil || manifest.Recipient.IdentityID != localIdentityID || manifest.Recipient.IdentityID != claim.Delivery.RecipientIdentityID || manifest.Recipient.EncryptionKeyID != claim.Delivery.RecipientKeyID || d.RecipientIdentityID != manifest.Recipient.IdentityID || d.RecipientKeyID != manifest.Recipient.EncryptionKeyID {
		return errors.New("manifest recipient binding does not match the authenticated inbox delivery")
	}
	if (manifest.Sender == nil) != (claim.Delivery.SenderIdentityID == nil) || (manifest.Sender == nil) != (claim.Delivery.SenderSigningKeyID == nil) || !equalOptionalString(d.SenderIdentityID, claim.Delivery.SenderIdentityID) || !equalOptionalString(d.SenderSigningKeyID, claim.Delivery.SenderSigningKeyID) {
		return errors.New("manifest sender presence does not match the authenticated inbox delivery")
	}
	if manifest.Sender != nil && (*claim.Delivery.SenderIdentityID != manifest.Sender.IdentityID || *claim.Delivery.SenderSigningKeyID != manifest.Sender.SigningKeyID || *d.SenderIdentityID != manifest.Sender.IdentityID || *d.SenderSigningKeyID != manifest.Sender.SigningKeyID) {
		return errors.New("manifest sender binding does not match the authenticated inbox delivery")
	}
	if (manifest.Sender == nil) != (claim.SenderSigningKey == nil) {
		return errors.New("claim sender signing-key snapshot presence does not match the manifest")
	}
	if (manifest.Sender == nil) != (claim.SenderIdentityState == nil) || claim.SenderIdentityState != nil && *claim.SenderIdentityState != "active" && *claim.SenderIdentityState != "disabled" {
		return errors.New("claim sender identity state does not match the manifest")
	}
	if manifest.Sender != nil && (claim.SenderSigningKey.IdentityID != manifest.Sender.IdentityID || claim.SenderSigningKey.KeyID != manifest.Sender.SigningKeyID || claim.SenderSigningKey.Purpose != "signing" || claim.SenderSigningKey.Algorithm != identitypkg.SigningAlgorithm) {
		return errors.New("claim sender signing-key snapshot does not match the manifest sender")
	}
	return nil
}

func equalOptionalInt64(a, b *int64) bool {
	return a == nil && b == nil || a != nil && b != nil && *a == *b
}

func equalOptionalString(a, b *string) bool {
	return a == nil && b == nil || a != nil && b != nil && *a == *b
}

func (a *app) verifyManifestSenderWithClaim(ctx context.Context, client *api.Client, manifest sealed.Manifest, claimed *api.IdentityKey, identityState *string) (string, error) {
	if manifest.Sender == nil || manifest.Signature == nil {
		return "Unknown sender", nil
	}
	if manifest.Signature.Algorithm != identitypkg.SigningAlgorithm {
		return "", errors.New("unsupported manifest signature algorithm")
	}
	if claimed == nil {
		return "", errors.New("authenticated claim omitted the sender signing-key snapshot")
	}
	sign := claimed
	if sign.KeyID != manifest.Sender.SigningKeyID || sign.IdentityID != manifest.Sender.IdentityID || sign.Purpose != "signing" || sign.Algorithm != identitypkg.SigningAlgorithm {
		return "", errors.New("manifest signing key record does not match its sender binding")
	}
	pub, err := identitypkg.ParsePublicKey(sign.PublicKey, ed25519.PublicKeySize)
	if err != nil {
		return "", err
	}
	signature, err := base64.RawURLEncoding.DecodeString(manifest.Signature.Value)
	if err != nil {
		return "", errors.New("invalid manifest signature encoding")
	}
	unsigned, err := sealed.CanonicalUnsignedManifest(manifest)
	if err != nil {
		return "", err
	}
	if !identitypkg.VerifyCanonical(ed25519.PublicKey(pub), identitypkg.ManifestSignatureDomain, unsigned, signature) {
		return "", errors.New("manifest signature verification failed")
	}
	var warnings []string
	if sign.RevokedAt != nil {
		warnings = append(warnings, "sender signing key is recorded as revoked")
	}
	if sign.NotAfter != nil && time.Now().Unix() > *sign.NotAfter {
		warnings = append(warnings, "sender signing key is expired")
	}
	if identityState == nil || *identityState != "active" {
		warnings = append(warnings, "sender identity is disabled")
	}
	if len(warnings) > 0 {
		return "Signature valid; " + strings.Join(warnings, "; "), nil
	}
	store, err := a.identityStore()
	if err != nil {
		return "", err
	}
	recipient, pinErr := store.Recipient(manifest.Sender.IdentityID)
	if pinErr == nil && recipient.SigningKeyID == sign.KeyID && (recipient.TrustState == "pinned" || recipient.TrustState == "rotated") {
		if err := ensureRecipientServer(recipient, client.BaseURL); err != nil {
			return "", err
		}
		pinnedPublic, parseErr := identitypkg.ParsePublicKey(recipient.SigningPublicKey, ed25519.PublicKeySize)
		if parseErr != nil {
			return "", fmt.Errorf("read pinned sender signing key: %w", parseErr)
		}
		if subtle.ConstantTimeCompare(pinnedPublic, pub) != 1 {
			return "", errors.New("authenticated sender signing key bytes differ from the locally pinned key")
		}
		return recipient.Alias + " (verified sender)", nil
	}
	return "Signature valid; sender unknown", nil
}

func (a *app) inboxRejectCmd() *cobra.Command {
	return a.inboxReceiptCommand("reject", "rejected", true)
}
func (a *app) inboxProcessedCmd() *cobra.Command {
	var identityRef string
	cmd := &cobra.Command{Use: "processed <delivery-id>", Short: "Submit a signed processed receipt after verified receipt", Args: exactArgs(1, "delivery ID"), RunE: func(cmd *cobra.Command, args []string) error {
		store, err := a.identityStore()
		if err != nil {
			return err
		}
		saved, err := store.LoadDeliveryContext(args[0])
		if err != nil {
			return fmt.Errorf("no verified receipt context for delivery: %w", err)
		}
		ref := saved.RecipientIdentityID
		if identityRef != "" {
			ref = identityRef
		}
		local, err := store.LoadIdentity(ref)
		if err != nil {
			return err
		}
		if local.IdentityID != saved.RecipientIdentityID {
			return errors.New("processing identity does not match verified recipient")
		}
		client, err := a.client()
		if err != nil {
			return err
		}
		if err := ensureLocalIdentityServer(local, client.BaseURL); err != nil {
			return err
		}
		claim := api.InboxClaim{ClaimID: saved.ClaimID, ClaimNonce: saved.ClaimNonce, Delivery: api.Delivery{ID: saved.DeliveryID, TransferID: saved.TransferID, RecipientIdentityID: saved.RecipientIdentityID, ManifestSHA256: saved.ManifestSHA256}}
		event, err := signReceipt(local, claim, "processed", saved.ManifestSHA256)
		if err != nil {
			return err
		}
		res, err := submitInboxReceiptSequence(cmd.Context(), client, store, saved.DeliveryID, "processed", []pendingInboxReceiptSubmission{{Event: event, IdempotencyKey: newIdempotencyKey()}}, false, nil)
		if err != nil {
			return err
		}
		if a.jsonOut {
			return a.printJSONValue(res.Value)
		}
		fmt.Fprintf(a.stdout, "processed delivery %s\n", saved.DeliveryID)
		return nil
	}}
	cmd.Flags().StringVar(&identityRef, "identity", "", "local recipient identity ID or handle")
	return cmd
}
func (a *app) inboxReceiptCommand(name, kind string, reject bool) *cobra.Command {
	var identityRef, manifestSHA string
	cmd := &cobra.Command{Use: name + " <delivery-id>", Short: "Submit a signed " + kind + " receipt", Args: exactArgs(1, "delivery ID"), RunE: func(cmd *cobra.Command, args []string) error {
		client, err := a.client()
		if err != nil {
			return err
		}
		claim, local, err := a.claimInbox(cmd.Context(), client, args[0], identityRef)
		if err != nil {
			return err
		}
		if manifestSHA == "" {
			manifestSHA = claim.Delivery.ManifestSHA256
		}
		event, err := signReceipt(local, claim, kind, manifestSHA)
		if err != nil {
			return err
		}
		store, storeErr := a.identityStore()
		if storeErr != nil {
			return storeErr
		}
		res, err := submitInboxReceiptSequence(cmd.Context(), client, store, claim.Delivery.ID, kind, []pendingInboxReceiptSubmission{{Event: event, Reject: reject, IdempotencyKey: newIdempotencyKey()}}, false, nil)
		if err != nil {
			return err
		}
		if reject {
			if storeErr := store.RemovePending("inbox-claim", claim.Delivery.ID); storeErr != nil {
				return fmt.Errorf("rejection accepted but pending claim state could not be removed: %w", storeErr)
			}
		}
		if a.jsonOut {
			return a.printJSONValue(res.Value)
		}
		fmt.Fprintf(a.stdout, "%s delivery %s\n", kind, args[0])
		return nil
	}}
	cmd.Flags().StringVar(&identityRef, "identity", "", "local recipient identity ID or handle")
	cmd.Flags().StringVar(&manifestSHA, "manifest-sha256", "", "expected manifest digest (default: delivery record)")
	return cmd
}

func (a *app) recoverCompletedInboxReceive(ctx context.Context, client *api.Client, store identitypkg.Store, claim api.InboxClaim, local identitypkg.LocalIdentity, outputDir string) (bool, error) {
	var pending pendingInboxReceiptSequence
	if err := store.LoadPending("receipt-receive", claim.Delivery.ID, &pending); errors.Is(err, os.ErrNotExist) {
		return false, nil
	} else if err != nil {
		return true, err
	}
	absoluteOutput, err := filepath.Abs(outputDir)
	if err != nil {
		return true, err
	}
	completion := &pendingInboxReceiptCompletion{OutputDir: filepath.Clean(absoluteOutput)}
	if pending.Completion == nil || pending.Completion.OutputDir != completion.OutputDir || pending.Completion.FileCount < 1 || len(pending.Submissions) != 2 || pending.Submissions[0].Event.Receipt.Type != "downloaded" || pending.Submissions[1].Event.Receipt.Type != "verified" {
		return true, errors.New("pending verified receive state is invalid or belongs to another output directory")
	}
	completion.FileCount = pending.Completion.FileCount
	for _, submission := range pending.Submissions {
		receipt := submission.Event.Receipt
		if receipt.Protocol != "aispace-delivery-receipt-v1" || receipt.DeliveryID != claim.Delivery.ID || receipt.TransferID != claim.Delivery.TransferID || receipt.RecipientIdentityID != local.IdentityID || receipt.ClaimID != claim.ClaimID || receipt.ClaimNonce != claim.ClaimNonce || receipt.ManifestSHA256 != claim.Delivery.ManifestSHA256 || submission.Reject {
			return true, errors.New("pending verified receipt does not match the recovered inbox claim")
		}
		private, keyErr := local.SigningPrivateFor(receipt.SigningKeyID)
		if keyErr != nil {
			return true, fmt.Errorf("pending receipt signing key is unavailable: %w", keyErr)
		}
		canonical, canonicalErr := identitypkg.CanonicalJSON(receipt)
		if canonicalErr != nil {
			return true, canonicalErr
		}
		signature, decodeErr := base64.RawURLEncoding.DecodeString(submission.Event.Signature)
		if decodeErr != nil || !identitypkg.VerifyCanonical(private.Public().(ed25519.PublicKey), identitypkg.ReceiptSignatureDomain, canonical, signature) {
			return true, errors.New("pending receipt signature is invalid")
		}
	}
	result, err := submitInboxReceiptSequence(ctx, client, store, claim.Delivery.ID, "receive", pending.Submissions, true, completion)
	if err != nil {
		return true, err
	}
	verified := pending.Submissions[1].Event.Receipt
	contextPath, err := store.SaveDeliveryContext(identitypkg.DeliveryContext{DeliveryID: verified.DeliveryID, TransferID: verified.TransferID, RecipientIdentityID: verified.RecipientIdentityID, SigningKeyID: verified.SigningKeyID, ClaimID: verified.ClaimID, ClaimNonce: verified.ClaimNonce, ManifestSHA256: verified.ManifestSHA256, VerifiedAt: time.Now().Unix()})
	if err != nil {
		return true, fmt.Errorf("verified receipt recovered but processing context could not be saved: %w", err)
	}
	if err := store.RemovePending("inbox-claim", claim.Delivery.ID); err != nil {
		return true, err
	}
	if err := store.RemovePending("receipt-receive", claim.Delivery.ID); err != nil {
		return true, err
	}
	if a.jsonOut {
		return true, a.printJSONValue(map[string]any{"receipt": result.Value, "recovered": true, "output": completion.OutputDir, "receipt_context": contextPath})
	}
	fmt.Fprintf(a.stdout, "Recovered verified delivery: %d file(s) already saved to %s\n", completion.FileCount, completion.OutputDir)
	return true, nil
}

func submitInboxReceiptSequence(ctx context.Context, client *api.Client, store identitypkg.Store, deliveryID, flow string, requested []pendingInboxReceiptSubmission, keepFinal bool, completion *pendingInboxReceiptCompletion) (api.Result[api.DeliveryReceiptEnvelope], error) {
	var pending pendingInboxReceiptSequence
	err := store.LoadPending("receipt-"+flow, deliveryID, &pending)
	if errors.Is(err, os.ErrNotExist) {
		pending = pendingInboxReceiptSequence{Version: 1, ServerURL: client.BaseURL, DeliveryID: deliveryID, Flow: flow, Submissions: requested, Completion: completion}
		for i := range pending.Submissions {
			if pending.Submissions[i].IdempotencyKey == "" {
				pending.Submissions[i].IdempotencyKey = newIdempotencyKey()
			}
		}
		if _, err := store.SavePending("receipt-"+flow, deliveryID, pending); err != nil {
			return api.Result[api.DeliveryReceiptEnvelope]{}, fmt.Errorf("cannot persist recoverable %s receipt: %w", flow, err)
		}
	} else if err != nil {
		return api.Result[api.DeliveryReceiptEnvelope]{}, err
	} else if !validPendingReceiptSequence(pending, client.BaseURL, deliveryID, flow, requested, completion) {
		return api.Result[api.DeliveryReceiptEnvelope]{}, errors.New("stored pending receipt sequence is invalid or does not match this delivery")
	}
	if pending.Next == len(pending.Submissions) {
		if pending.Final == nil {
			return api.Result[api.DeliveryReceiptEnvelope]{}, errors.New("completed pending receipt sequence omitted its final response")
		}
		result := api.Result[api.DeliveryReceiptEnvelope]{Value: *pending.Final}
		if !keepFinal {
			if err := store.RemovePending("receipt-"+flow, deliveryID); err != nil {
				return api.Result[api.DeliveryReceiptEnvelope]{}, err
			}
		}
		return result, nil
	}
	var result api.Result[api.DeliveryReceiptEnvelope]
	for pending.Next < len(pending.Submissions) {
		submission := pending.Submissions[pending.Next]
		result, err = client.SubmitInboxReceipt(ctx, deliveryID, submission.Event, submission.Reject, submission.IdempotencyKey)
		if err != nil {
			return api.Result[api.DeliveryReceiptEnvelope]{}, fmt.Errorf("%s receipt response was not recovered; exact signed replay state was retained: %w", submission.Event.Receipt.Type, err)
		}
		pending.Next++
		if pending.Next == len(pending.Submissions) {
			pending.Final = &result.Value
		}
		if _, err := store.SavePending("receipt-"+flow, deliveryID, pending); err != nil {
			return api.Result[api.DeliveryReceiptEnvelope]{}, fmt.Errorf("receipt acknowledged but replay progress could not be saved: %w", err)
		}
	}
	if !keepFinal {
		if err := store.RemovePending("receipt-"+flow, deliveryID); err != nil {
			return api.Result[api.DeliveryReceiptEnvelope]{}, fmt.Errorf("receipt acknowledged but replay state could not be removed: %w", err)
		}
	}
	return result, nil
}

func validPendingReceiptSequence(pending pendingInboxReceiptSequence, serverURL, deliveryID, flow string, requested []pendingInboxReceiptSubmission, completion *pendingInboxReceiptCompletion) bool {
	if pending.Version != 1 || pending.ServerURL != serverURL || pending.DeliveryID != deliveryID || pending.Flow != flow || pending.Next < 0 || pending.Next > len(pending.Submissions) || len(pending.Submissions) != len(requested) || len(pending.Submissions) == 0 {
		return false
	}
	if (pending.Completion == nil) != (completion == nil) || pending.Completion != nil && (pending.Completion.OutputDir != completion.OutputDir || pending.Completion.FileCount != completion.FileCount) {
		return false
	}
	for i, saved := range pending.Submissions {
		want := requested[i]
		if saved.IdempotencyKey == "" || saved.Reject != want.Reject || saved.Event.Signature == "" || saved.Event.Receipt.ReceiptID == "" || !sameReceiptBinding(saved.Event.Receipt, want.Event.Receipt) {
			return false
		}
	}
	return true
}

func sameReceiptBinding(a, b api.SignedDeliveryReceipt) bool {
	return a.Protocol == b.Protocol && a.DeliveryID == b.DeliveryID && a.TransferID == b.TransferID && a.Type == b.Type && a.RecipientIdentityID == b.RecipientIdentityID && a.ClaimID == b.ClaimID && a.ClaimNonce == b.ClaimNonce && a.ManifestSHA256 == b.ManifestSHA256
}
