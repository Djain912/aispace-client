package cmd

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/aispace-sh/aispace-client/internal/api"
	"github.com/aispace-sh/aispace-client/internal/handoff"
)

const pairingAttemptLifetime = 5 * time.Minute

func (a *app) handoffCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "handoff",
		Short: "Move a sealed transfer safely between humans and devices",
		Args:  noArgs,
	}
	cmd.AddCommand(a.handoffEncodeCmd(), a.handoffOfferCmd(), a.handoffReceiveCmd())
	return cmd
}

func (a *app) handoffEncodeCmd() *cobra.Command {
	var tokenFile string
	cmd := &cobra.Command{
		Use:   "encode [https-link-or-token] [--token-file PATH]",
		Short: "Show equivalent HTTPS, CLI-token, and native-link representations",
		Long: "Reads a bearer transfer reference from a mode-0600 file, AISPACE_TRANSFER_TOKEN,\n" +
			"the protected prompt, or one convenience argument. Process arguments may be visible\n" +
			"to other local users, so prefer --token-file, the environment, or the prompt.",
		Args: func(_ *cobra.Command, args []string) error {
			if len(args) > 1 {
				return usagef("expected at most one transfer reference")
			}
			return nil
		},
		RunE: func(_ *cobra.Command, args []string) error {
			ref, err := a.readTransferReference(args, tokenFile)
			if err != nil {
				return err
			}
			if len(args) == 1 {
				fmt.Fprintln(a.stderr, "warning: bearer transfer references in process arguments may be visible to other local users; prefer --token-file, AISPACE_TRANSFER_TOKEN, or the prompt")
			}
			cfg, err := a.resolve()
			if err != nil {
				return err
			}
			intent, err := handoff.Parse(ref, cfg.URL)
			if err != nil {
				return usagef("invalid transfer reference: %v", err)
			}
			if intent.Mode != handoff.ModeSealedLink {
				return usagef("only sealed-link handoffs can be encoded in v1")
			}
			httpsLink, _ := intent.HTTPSLink()
			token, _ := intent.Token()
			deep, _ := intent.DeepLink()
			if a.jsonOut {
				return usagef("handoff secrets are not emitted in JSON mode")
			}
			fmt.Fprintf(a.stdout, "origin  %s\nmode    %s\nhttps   %s\ntoken   %s\nnative  %s\n", intent.ServiceOrigin, intent.Mode, httpsLink, token, deep)
			return nil
		},
	}
	cmd.Flags().StringVar(&tokenFile, "token-file", "", "read the link or token from a mode-0600 file")
	return cmd
}

func (a *app) handoffOfferCmd() *cobra.Command {
	var ticketPath string
	cmd := &cobra.Command{
		Use:   "offer <transfer-id>",
		Short: "Create a five-minute code and hand the transfer to one approved device",
		Args:  exactArgs(1, "<transfer-id>"),
		RunE: func(cmd *cobra.Command, args []string) error {
			if a.jsonOut {
				return usagef("interactive handoff is unavailable in JSON mode")
			}
			if ticketPath == "" {
				var err error
				ticketPath, err = a.transferTicketPath(args[0])
				if err != nil {
					return err
				}
			}
			ticket, err := readTransferTicket(ticketPath)
			if err != nil {
				return err
			}
			if ticket.TransferID != args[0] || ticket.RecipientToken == "" {
				return usagef("ticket cannot hand off transfer %s", args[0])
			}
			client, err := a.clientForTransferTicket(ticket)
			if err != nil {
				return err
			}
			intent, err := handoff.Parse(ticket.RecipientToken, ticket.ServerURL)
			if err != nil {
				return &codedError{code: "ticket", err: errors.New("ticket contains an invalid handoff token"), exit: ExitGeneric}
			}
			token, err := intent.Token()
			if err != nil {
				return err
			}
			pair, err := client.CreatePairingCode(cmd.Context(), ticket.TransferID, newIdempotencyKey())
			if err != nil {
				return err
			}
			if pair.Value.DeviceID == "" || pair.Value.DeviceCapability == "" || pair.Value.Code == "" {
				return &codedError{code: "bad_response", err: errors.New("pairing response omitted code or device capability"), exit: ExitGeneric}
			}
			if err := requirePairingState(pair.Value.State, "pending"); err != nil {
				return err
			}
			fmt.Fprintf(a.stdout, "code    %s\nopen    %s/pair/%s\nexpires %s\nwaiting for one receiver to approve…\n", pair.Value.Code, client.BaseURL, pair.Value.Code, fmtTime(pair.Value.ExpiresAt))
			failures := 0
			for {
				if time.Now().Unix() >= pair.Value.ExpiresAt {
					return &codedError{code: "pairing_expired", err: errors.New("pairing code expired"), exit: ExitGeneric}
				}
				status, pollErr := client.PollPairingDevice(cmd.Context(), pair.Value.DeviceID, pair.Value.DeviceCapability)
				if pollErr != nil {
					if !transientHandoffFailure(pollErr) {
						return pollErr
					}
					failures++
					if err := waitHandoff(cmd.Context(), handoffRetryDuration(pair.Value.PollInterval, failures, pair.Value.ExpiresAt)); err != nil {
						return err
					}
					continue
				}
				failures = 0
				action, stateErr := devicePairingAction(status.Value)
				if stateErr != nil {
					return stateErr
				}
				if action == "bind" {
					envelope, sealErr := handoff.SealForReceiver(*status.Value.ReceiverPublicKey, *status.Value.AttemptID, []byte(token))
					if sealErr != nil {
						return sealErr
					}
					binding, bindErr := client.BindPairing(cmd.Context(), pair.Value.DeviceID, pair.Value.DeviceCapability, *status.Value.AttemptID, envelope)
					if bindErr != nil {
						if !transientHandoffFailure(bindErr) {
							return bindErr
						}
						failures++
						if err := waitHandoff(cmd.Context(), handoffRetryDuration(pair.Value.PollInterval, failures, pair.Value.ExpiresAt)); err != nil {
							return err
						}
						continue
					}
					if err := requirePairingState(binding.Value.State, "ready"); err != nil {
						return err
					}
					fmt.Fprintln(a.stdout, "ready   encrypted transfer intent delivered to the approved device")
					return nil
				}
				if action == "done" {
					fmt.Fprintln(a.stdout, "ready   encrypted transfer intent delivered to the approved device")
					return nil
				}
				if err := waitHandoff(cmd.Context(), pollDuration(pair.Value.PollInterval)); err != nil {
					return err
				}
			}
		},
	}
	cmd.Flags().StringVar(&ticketPath, "ticket", "", "owner ticket path (default: saved ticket for this transfer)")
	return cmd
}

func (a *app) handoffReceiveCmd() *cobra.Command {
	var yes, overwrite bool
	var outputDir string
	cmd := &cobra.Command{
		Use:   "receive <pairing-code>",
		Short: "Inspect, approve, and receive a transfer from a nearby device",
		Args:  exactArgs(1, "<pairing-code>"),
		RunE: func(cmd *cobra.Command, args []string) error {
			if a.jsonOut {
				return usagef("interactive handoff is unavailable in JSON mode")
			}
			code, err := canonicalPairCode(args[0])
			if err != nil {
				return usagef("invalid pairing code: %v", err)
			}
			cfg, err := a.resolve()
			if err != nil {
				return err
			}
			client := api.New(cfg.URL, "", a.userAgent())
			receiver, publicKey, err := handoff.NewReceiverKey()
			if err != nil {
				return err
			}
			nonce, err := randomHandoffNonce()
			if err != nil {
				return err
			}
			attemptDeadline := time.Now().Add(pairingAttemptLifetime).Unix()
			attempt, err := attemptPairingWithRetry(cmd.Context(), client, code, publicKey, nonce, attemptDeadline)
			if err != nil {
				return err
			}
			if err := requirePairingState(attempt.Value.State, "awaiting_approval"); err != nil {
				return err
			}
			attemptOrigin, attemptOriginErr := handoff.CanonicalOrigin(attempt.Value.Intent.Origin)
			configuredOrigin, configuredOriginErr := handoff.CanonicalOrigin(cfg.URL)
			if attemptOriginErr != nil || configuredOriginErr != nil || attemptOrigin != configuredOrigin {
				return &codedError{code: "origin_mismatch", err: errors.New("pairing intent origin does not match the configured service"), exit: ExitUsage}
			}
			fmt.Fprintf(a.stdout, "origin  %s\nmode    %s\nsize    %s\nfiles   %d\nexpires %s\nsender  %s\n",
				attempt.Value.Intent.Origin, attempt.Value.Intent.Mode, fmtBytes(attempt.Value.Intent.DeclaredPlaintextBytes), attempt.Value.Intent.FileCount,
				fmtTime(attempt.Value.Intent.ExpiresAt), handoffSender(attempt.Value.Intent.Sender))
			if !yes {
				fmt.Fprint(a.stderr, "Approve this handoff? [y/N] ")
				line, _ := readOneLine(a.stdin)
				answer := strings.ToLower(strings.TrimSpace(line))
				if answer != "y" && answer != "yes" {
					return &codedError{code: "declined", err: errors.New("handoff declined before receiving a secret"), exit: ExitGeneric}
				}
			}
			approval, err := approvePairingWithRetry(cmd.Context(), client, attempt.Value.AttemptID, attempt.Value.AttemptCapability, attempt.Value.ExpiresAt, attempt.Value.PollInterval)
			if err != nil {
				return err
			}
			if err := requirePairingState(approval.Value.State, "approved", "ready"); err != nil {
				return err
			}
			failures := 0
			for {
				if time.Now().Unix() >= attempt.Value.ExpiresAt {
					return &codedError{code: "pairing_expired", err: errors.New("pairing code expired"), exit: ExitGeneric}
				}
				status, pollErr := client.PollPairingAttempt(cmd.Context(), attempt.Value.AttemptID, attempt.Value.AttemptCapability)
				if pollErr != nil {
					if !transientHandoffFailure(pollErr) {
						return pollErr
					}
					failures++
					if err := waitHandoff(cmd.Context(), handoffRetryDuration(attempt.Value.PollInterval, failures, attempt.Value.ExpiresAt)); err != nil {
						return err
					}
					continue
				}
				failures = 0
				action, stateErr := attemptPairingAction(status.Value)
				if stateErr != nil {
					return stateErr
				}
				if action == "open" {
					plaintext, openErr := receiver.Open(attempt.Value.AttemptID, *status.Value.Envelope)
					if openErr != nil {
						return &codedError{code: "authentication", err: openErr, exit: ExitGeneric}
					}
					intent, parseErr := handoff.Parse(string(plaintext), cfg.URL)
					if parseErr != nil || intent.TransferID != attempt.Value.Intent.TransferID || string(intent.Mode) != attempt.Value.Intent.Mode {
						return &codedError{code: "intent_mismatch", err: errors.New("received intent does not match the approved summary"), exit: ExitGeneric}
					}
					return a.runTransferReceive(cmd.Context(), string(plaintext), outputDir, true, overwrite)
				}
				if err := waitHandoff(cmd.Context(), pollDuration(attempt.Value.PollInterval)); err != nil {
					return err
				}
			}
		},
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "approve the displayed handoff without prompting")
	cmd.Flags().StringVarP(&outputDir, "output", "o", ".", "destination directory")
	cmd.Flags().BoolVar(&overwrite, "overwrite", false, "replace existing destination files only after verification")
	return cmd
}

func canonicalPairCode(raw string) (string, error) {
	normalized := strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(raw), "-", ""))
	if len(normalized) != 8 {
		return "", errors.New("expected eight characters")
	}
	for _, character := range normalized {
		if !strings.ContainsRune("23456789ABCDEFGHJKMNPQRSTUVWXYZ", character) {
			return "", errors.New("code contains an ambiguous or invalid character")
		}
	}
	return normalized[:4] + "-" + normalized[4:], nil
}

func randomHandoffNonce() (string, error) {
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(nonce[:]), nil
}

func pollDuration(seconds int) time.Duration {
	if seconds < 1 {
		seconds = 5
	}
	return time.Duration(seconds) * time.Second
}

func handoffRetryDuration(seconds, failures int, expiresAt int64) time.Duration {
	base := pollDuration(seconds)
	if failures > 5 {
		failures = 5
	}
	duration := base * time.Duration(1<<failures)
	if duration > 30*time.Second {
		duration = 30 * time.Second
	}
	remaining := time.Until(time.Unix(expiresAt, 0))
	if remaining > 0 && duration > remaining {
		return remaining
	}
	return duration
}

func transientHandoffFailure(err error) bool {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var apiErr *api.Error
	if errors.As(err, &apiErr) {
		if apiErr.Code == "network" {
			return true
		}
		return apiErr.Status == 408 || apiErr.Status == 429 || apiErr.Status >= 500
	}
	var networkErr net.Error
	return errors.As(err, &networkErr)
}

func attemptPairingWithRetry(ctx context.Context, client *api.Client, code, publicKey, nonce string, expiresAt int64) (api.Result[api.PairingAttempt], error) {
	var empty api.Result[api.PairingAttempt]
	failures := 0
	for {
		if failures > 0 && time.Now().Unix() >= expiresAt {
			return empty, pairingExpiredError()
		}
		result, err := client.AttemptPairing(ctx, code, publicKey, nonce)
		if err == nil {
			return result, nil
		}
		if !transientHandoffFailure(err) {
			return empty, err
		}
		if time.Now().Unix() >= expiresAt {
			return empty, pairingExpiredError()
		}
		failures++
		if err := waitHandoff(ctx, handoffRetryDuration(1, failures-1, expiresAt)); err != nil {
			return empty, err
		}
	}
}

func approvePairingWithRetry(ctx context.Context, client *api.Client, attemptID, capability string, expiresAt int64, pollInterval int) (api.Result[api.PairingApproval], error) {
	var empty api.Result[api.PairingApproval]
	failures := 0
	for {
		if failures > 0 && time.Now().Unix() >= expiresAt {
			return empty, pairingExpiredError()
		}
		result, err := client.ApprovePairing(ctx, attemptID, capability)
		if err == nil {
			return result, nil
		}
		if !transientHandoffFailure(err) {
			return empty, err
		}
		if time.Now().Unix() >= expiresAt {
			return empty, pairingExpiredError()
		}
		failures++
		if err := waitHandoff(ctx, handoffRetryDuration(pollInterval, failures-1, expiresAt)); err != nil {
			return empty, err
		}
	}
}

func devicePairingAction(status api.PairingDeviceStatus) (string, error) {
	switch status.State {
	case "pending", "attempted":
		return "wait", nil
	case "approved":
		if status.AttemptID == nil || *status.AttemptID == "" || status.ReceiverPublicKey == nil || *status.ReceiverPublicKey == "" {
			return "", &codedError{code: "bad_response", err: errors.New("approved pairing omitted receiver identity"), exit: ExitGeneric}
		}
		return "bind", nil
	case "bound":
		return "done", nil
	case "crowded":
		return "", &codedError{code: "pairing_crowded", err: errors.New("more than one receiver entered the code; create a new handoff"), exit: ExitGeneric}
	case "expired":
		return "", pairingExpiredError()
	case "revoked":
		return "", &codedError{code: "pairing_revoked", err: errors.New("pairing code was revoked"), exit: ExitGeneric}
	default:
		return "", unexpectedPairingState(status.State)
	}
}

func attemptPairingAction(status api.PairingAttemptStatus) (string, error) {
	switch status.State {
	case "attempted", "approved":
		return "wait", nil
	case "bound":
		if status.Envelope == nil || *status.Envelope == "" {
			return "", &codedError{code: "bad_response", err: errors.New("bound pairing omitted encrypted envelope"), exit: ExitGeneric}
		}
		return "open", nil
	case "crowded":
		return "", &codedError{code: "pairing_crowded", err: errors.New("pairing was invalidated because another receiver entered the code"), exit: ExitGeneric}
	case "expired":
		return "", pairingExpiredError()
	case "revoked":
		return "", &codedError{code: "pairing_revoked", err: errors.New("pairing code was revoked"), exit: ExitGeneric}
	default:
		return "", unexpectedPairingState(status.State)
	}
}

func pairingExpiredError() error {
	return &codedError{code: "pairing_expired", err: errors.New("pairing code expired"), exit: ExitGeneric}
}

func unexpectedPairingState(state string) error {
	return &codedError{code: "bad_response", err: fmt.Errorf("pairing service returned unknown state %q", state), exit: ExitGeneric}
}

func requirePairingState(state string, allowed ...string) error {
	for _, candidate := range allowed {
		if state == candidate {
			return nil
		}
	}
	switch state {
	case "expired":
		return pairingExpiredError()
	case "revoked":
		return &codedError{code: "pairing_revoked", err: errors.New("pairing code was revoked"), exit: ExitGeneric}
	case "crowded":
		return &codedError{code: "pairing_crowded", err: errors.New("pairing was invalidated because another receiver entered the code"), exit: ExitGeneric}
	}
	return unexpectedPairingState(state)
}

func waitHandoff(ctx context.Context, duration time.Duration) error {
	if sleep != nil {
		sleep(duration)
		return ctx.Err()
	}
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func handoffSender(sender *string) string {
	if sender == nil || *sender == "" {
		return "Unknown sender"
	}
	return *sender
}
