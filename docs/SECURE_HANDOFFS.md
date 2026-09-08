# Secure handoffs

Use this guide when a file handoff needs more than a conventional upload link. The same sealed
format supports an anonymous bearer link, a delivery to a trusted agent identity, and a temporary
handoff to another device. Adaptive mode is currently a durable-first delivery intent: the
official client still transfers bytes through R2 immediately.

For every flag, see [CLI.md](CLI.md). For HTTP request and response shapes, see [API.md](API.md).

## Choose a workflow

| Need | Use | Recipient gets |
|---|---|---|
| Send an encrypted bundle to anyone | `transfer create --sealed --link` | A bearer HTTPS link or CLI token |
| Send to a known offline agent | `transfer create --sealed --to ALIAS` | An item in its authenticated inbox |
| Move an existing transfer to a nearby device | `handoff offer` | A five-minute pairing code |
| Record a preference for future live transport | `--transport adaptive` | The same durable R2 transfer today |

These workflows are experimental and can be disabled by the service operator. Sealed transfers
are the base feature. Agent identities, pairing, and adaptive intent each require an additional
server-side feature gate. A disabled optional feature does not make ordinary uploads or R2 storage
unavailable.

## Send an anonymous sealed bundle

```sh
aispace transfer create report.pdf charts.png \
  --sealed --link --expires 1d --max-downloads 1
```

`transfer create`:

1. Inspects and hashes regular files locally.
2. Creates a private manifest containing filenames, content types, per-file sizes, and hashes.
3. Encrypts the manifest and each 8 MiB plaintext chunk locally with `aispace-sealed-v1`.
4. Uploads ciphertext to a resumable R2 multipart transfer and finalizes it durably.
5. Saves a mode-`0600` owner ticket containing the resume and revoke capabilities.
6. Prints the expiry plus an HTTPS recipient link.

The service sees aggregate declared sizes, file and part counts, expiry, and download policy. It
does not receive the master key, private manifest, filenames, content types, or plaintext hashes.

The HTTPS link and canonical CLI token contain equivalent bearer authority. The link looks like:

```text
https://aispace.sh/t/<transfer-id>#as1.<master-key>.<claim-capability>
```

The browser does not send the fragment after `#` in its HTTP request. It is still a secret when
copied through chat, email, logs, screenshots, or link-inspection software. Anyone who obtains the
whole reference can claim and decrypt the transfer until it expires, is revoked, or reaches its
verified-download cap.

### Capture a secret for automation

JSON output omits bearer links and tokens by default. Opt in only when stdout is redirected to a
protected file:

```sh
umask 077
aispace transfer create report.pdf --sealed --link \
  --json --include-secret > handoff.json
jq -r .token handoff.json > handoff.token
chmod 600 handoff.token
```

`--include-secret` requires `--json`, and the CLI refuses it when stdout is a terminal. The normal
JSON result still includes the transfer, ticket path, digests, and selected transport without
including the bearer link or token.

> A sealed anonymous transfer proves that the manifest and bytes decrypt and verify. It does not
> authenticate the sender. Recipients must see `Unknown sender` unless a signed identity is
> present and trusted.

## Receive and verify

The safest interactive path keeps the secret out of process arguments:

```sh
aispace transfer receive
# Paste link or token: …
```

For automation, use a mode-`0600` token file or `AISPACE_TRANSFER_TOKEN`:

```sh
aispace transfer receive --token-file ./handoff.token \
  --output ./received --yes
```

The receiver authenticates and decrypts the manifest before creating a server-side claim. It
shows the filenames, sizes, expiry, download policy, and sender status before confirmation. During
download it writes mode-`0600` partial files, resumes transient reads from authenticated chunk
boundaries, verifies every chunk tag and complete-file SHA-256, and only then installs the final
names. Existing paths are refused unless `--overwrite` is explicit.

A limited download is consumed only after cryptographic verification and an idempotent commit.
An explicitly released claim becomes available again immediately; an interrupted claim becomes
available after its lease expires.

The browser receiver performs the same manifest, chunk, and hash checks. It removes the fragment
from the visible URL and retains the master key only in memory. Browser download support depends
on available safe streaming APIs; unsupported multi-file or large transfers direct the recipient
to the CLI before creating a claim.

## Recover or revoke

The owner ticket is the recovery record for a sealed transfer:

```sh
aispace transfer status "$TRANSFER_ID"
aispace transfer resume "$TRANSFER_ID"
aispace transfer revoke "$TRANSFER_ID"
```

`resume` re-hashes the original source paths recorded in the ticket, compares uploaded part sizes
and SHA-256 values, uploads only missing valid parts, replaces the encrypted manifest, and
idempotently completes the transfer. Do not change or remove the source files until upload has
completed.

`revoke` reads the transfer-scoped revoke capability from the saved ticket instead of placing it
in a process argument. Losing the ticket removes the CLI resume and revoke path. Losing the
recipient token or identity private key makes the ciphertext unrecoverable; aispace has no key
escrow.

## Create a trusted agent identity

The bot key needs the appropriate identity scopes before it can create or administer an identity.
Create the private keys with the CLI, not the dashboard:

```sh
aispace identity create \
  --name "Research agent" --handle research-agent
```

The CLI generates X25519 encryption and Ed25519 signing keys locally. It writes the private record
beside the CLI configuration with mode `0600` and uploads only public keys plus proof-of-possession
signatures. Back up the private record securely. The service cannot recreate it, and losing it can
make unread deliveries unrecoverable.

The public address uses the account-scoped form `research-agent@acct_…`. It is not an email
address or a globally unique vanity name; senders still import an exact invitation and pin its
fingerprint.

The dashboard displays identity status, the active public fingerprint, an invitation, key history,
bot-key bindings, inbox policy, delivery activity, and receipts. It never generates, accepts, or
displays an identity private key. Bind the recipient identity to the bot keys that should list and
claim its inbox, and grant only the required scopes.

Rotation is authorized by the predecessor key:

```sh
aispace identity rotate research-agent --purpose encryption
aispace identity rotate research-agent --purpose signing
```

Old encryption private keys remain in the local record so already-addressed deliveries are still
readable. Revoking a public key prevents new use; it does not erase ciphertext or keys already held
by another party. Disabling the identity stops public lookup and new deliveries.

## Pin a recipient and send to its inbox

The recipient copies its invitation from the dashboard. The invitation fragment is a public
fingerprint, not a decryption key. Put the exact copied values in protected shell variables, then
import the invitation and compare the complete fingerprint through another trusted channel before
pinning it:

```sh
aispace recipient add "$RECIPIENT_INVITATION"

aispace recipient verify research-agent \
  --fingerprint "$RECIPIENT_FINGERPRINT"
```

Import verifies that the published keys match the invitation fragment, but the recipient remains
`unverified` until the explicit fingerprint comparison. On each send, the CLI resolves the public
record again and accepts only the pinned keys or a complete predecessor-signed rotation chain.
Unexpected replacements are marked changed and stop the send.

Address the transfer to the pinned alias:

```sh
aispace transfer create report.pdf \
  --sealed --to research-agent --from my-agent --expires 7d
```

The master key is wrapped to the recipient's active encryption key. `--from` signs the private
manifest with a local sender identity. No anonymous bearer decryption link is printed. Add
`--also-link` only when you intentionally want a second anonymous recovery route.

An automation may use `--trust-on-first-use` or supply `--recipient-fingerprint`, but an expected
fingerprint is the safer noninteractive choice. Display names and valid unknown signatures are not
trust anchors.

## Receive from an addressed inbox

The receiving bot key must be bound to the local identity and have the relevant inbox and receipt
scopes:

```sh
aispace inbox list
aispace inbox receive "$DELIVERY_ID" --yes
```

`inbox receive` unwraps the content key locally, checks that the claimed delivery and encrypted
manifest agree, verifies sender and recipient bindings, authenticates every chunk and file, then
submits signed `downloaded` and `verified` receipts.

Service authorization and cryptographic trust are deliberately separate. A valid signature from
an unpinned key is reported as `Signature valid; sender unknown`. With `--yes`, the CLI refuses an
unsigned, unknown, revoked, expired, or disabled sender unless `--allow-unknown-sender` is also
explicit. Interactive mode can display the warning and ask the operator.

That signature result comes from the CLI after it decrypts the private manifest. The dashboard
cannot decrypt the manifest or independently verify its signature; it projects server-visible
states such as `Pinned sender`, `Changed key`, or revoked.

After the recipient application accepts the files, it can record that separate stage:

```sh
aispace inbox processed "$DELIVERY_ID"
```

`processed` is an application acknowledgement, not proof that later work was correct. To reject a
delivery without installing its files:

```sh
aispace inbox reject "$DELIVERY_ID"
```

Receipt requests are signed and persisted before submission. A restart replays the exact receipt
ID, signature, claim nonce, and idempotency key. Senders can inspect delivery and receipt history
in the dashboard or through `GET /v1/deliveries/:id/receipts`.

## Hand off to one nearby device

Start from a completed anonymous sealed transfer. The sender needs its saved owner ticket and must
remain running:

```sh
aispace handoff offer "$TRANSFER_ID"
# code    J7KM-PQRT
# open    https://aispace.sh/pair/J7KM-PQRT
```

On the other CLI:

```sh
aispace handoff receive J7KM-PQRT --output ./incoming
```

The receiver first sees the exact service origin, transfer mode, sender status, aggregate size,
file count, and expiry. After approval, the sender encrypts the canonical transfer intent to the
receiver's one-use P-256 device key. The service relays only the encrypted envelope.

The code expires after five minutes and admits one approved receiver. It is not the transfer key.
A second distinct receiver crowds and invalidates the room, so both users must restart. `--yes`
skips only the approval prompt; it does not weaken origin, envelope, manifest, chunk, or file
verification. Pairing is unavailable in JSON mode.

For copy-and-paste handoff, show the equivalent representations without contacting a pairing
room:

```sh
aispace handoff encode
aispace handoff encode --token-file ./handoff.token
```

The command reads the reference from a protected prompt, file, or environment variable and prints
equivalent HTTPS, CLI-token, and native-link text. The native URI is only a representation; native
OS scheme registration and universal/app-link packaging are not included.

## Request adaptive durable-first delivery

```sh
aispace transfer create report.pdf --sealed --link \
  --transport adaptive --durability durable-first \
  --transport-privacy relay-only
```

The official client first discovers whether the service accepts adaptive intent. When compatible,
it records `transport_mode=adaptive`; when discovery is absent, malformed, or temporarily
unavailable, it sends the strict stored request instead. In both cases it immediately starts and
completes the durable R2 upload.

No reviewed live byte driver ships in the current client. Direct and TURN capabilities remain
unavailable, live-session creation fails closed, and the selected transport remains R2.
`direct-first` and `live-only` are rejected. `relay-only` is the safer policy for future live
support; a future direct mode may expose network addresses to the peer and signaling
infrastructure.

Adaptive intent therefore does not currently improve transfer speed. It records an operator- and
client-compatible preference without delaying, replacing, or weakening durable storage.

## Use a self-hosted origin

Set the same exact HTTPS origin on each participating CLI:

```sh
export AISPACE_URL=https://files.example.com
export AISPACE_KEY=ask_...

aispace transfer create report.pdf --sealed --link
```

Sealed tokens, owner tickets, identity files, recipient invitations, and pairing summaries are
origin-bound. The client rejects using those records against a different configured service. An
identity invitation must use HTTPS, contain no credentials or query, and match the configured
origin exactly.

A self-hosted operator must deploy the sealed-transfer migrations and bindings, then explicitly
enable each experimental feature. Identity requires sealed delivery. Handoff and adaptive intent
also require sealed delivery. Pairing additionally requires the pairing Durable Object. Core R2
delivery works without any future signaling or TURN component.

## Current limits and deferred work

The current release provides:

- sealed, resumable, multi-file ciphertext storage and verified claims;
- CLI and browser anonymous receive flows;
- CLI-held agent identities, fingerprint pins, addressed inboxes, and signed receipts;
- five-minute, one-approved-device handoff using an encrypted device envelope;
- adaptive durable-first intent with immediate R2 fallback.

It does **not** provide:

- live WebRTC data channels, authenticated live signaling, TURN credentials, or native byte relay;
- `direct-first`, `live-only`, or ephemeral delivery;
- built-in QR generation;
- PAKE-based short-code rendezvous;
- universal-link packaging or native OS scheme registration;
- browser-held agent identity private keys, multi-device identities, or server-side key recovery;
- multi-recipient delivery or signed outbound webhooks.

Legacy `aispace upload --encrypt` is a separate single-file age X25519 workflow. It produces an
encrypted file whose age identity must be delivered separately. It is not the sealed transfer,
addressed inbox, receipt, or device-handoff protocol described here.

## Security checklist

- Treat a sealed link, CLI token, owner ticket, bot key, age identity, and agent identity file as
  credentials with different scopes.
- Prefer protected prompts, mode-`0600` files, or secret environment variables over process
  arguments for bearer references.
- Compare a recipient's complete fingerprint through a separate trusted channel before pinning.
- Keep identity private records backed up and bound to their original server origin.
- Do not interpret encryption as sender authentication or `processed` as proof of correct work.
- Revoke a transfer when a link, token, pairing display, or owner workflow may have been exposed.
- Remember that expiry and revocation stop future access; they cannot recall plaintext already
  downloaded or keys already copied.
