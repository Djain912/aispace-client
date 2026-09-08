# aispace client API

The CLI uses the public bot API at `https://aispace.sh`. Compatible endpoints can be selected with
`AISPACE_URL` or `--url`.

For end-to-end workflows and security decisions, start with
[Secure handoffs](SECURE_HANDOFFS.md). This page is the wire-level endpoint reference.

Experimental endpoint groups can be disabled independently by the service operator. Agent
identity requires sealed transfers; human/device handoff and adaptive intent also require sealed
transfers. The adaptive endpoints currently expose discovery and fail-closed control-plane
behavior only: no WebRTC, TURN, native live relay, or other live byte path is available.

## Authentication

Send a bot key as a bearer token:

```http
Authorization: Bearer ask_XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX
```

Never put a bot key in a URL. JSON errors use the shape
`{"error":{"code":"...","message":"..."}}`.

## Endpoints

| Method | Path | Purpose |
|---|---|---|
| `GET` | `/v1/whoami` | Validate a key and identify its account |
| `GET` | `/v1/quota` | Read storage, file-size, request, and monthly limits |
| `POST` | `/v1/files` | Upload a raw request body |
| `GET` | `/v1/files` | List files using cursor pagination |
| `GET` | `/v1/files/:id` | Read file metadata |
| `GET` | `/v1/files/:id/content` | Authenticated download of an owned or account-shared file |
| `DELETE` | `/v1/files/:id` | Delete a file and invalidate its links |
| `POST` | `/v1/files/:id/links` | Create a Pro expiring public share link |
| `GET` | `/v1/files/:id/links` | List link metadata; tokens are not returned |
| `DELETE` | `/v1/links/:id` | Revoke a share link |
| `GET`, `HEAD` | `/d/:token` | Download through a public share link |
| `POST` | `/v1/transfers` | Reserve an authenticated sealed transfer |
| `GET` | `/v1/transfers/:id` | Read owner-visible state and uploaded parts |
| `PUT` | `/v1/transfers/:id/parts/:number` | Upload an exact ciphertext part |
| `PUT` | `/v1/transfers/:id/manifest` | Upload the nonce-prefixed encrypted manifest |
| `POST` | `/v1/transfers/:id/complete` | Atomically finalize uploaded parts and manifest |
| `DELETE` | `/v1/transfers/:id` | Abort or revoke with an owner capability |
| `GET` | `/v1/transports` | Discover transport, privacy, durability, and kill-switch capabilities |
| `POST` | `/v1/transfers/:id/live-sessions` | Create a short-lived adaptive negotiation intent |
| `POST` | `/v1/transfers/:id/live-sessions/:session_id/close` | Record a bounded selected path or R2 fallback outcome |
| `POST` | `/v1/transfers/:id/pairing-codes` | Create a five-minute pairing room for an owned anonymous sealed transfer |
| `DELETE` | `/v1/transfers/:transfer_id/pairing-codes/:pairing_id` | Revoke an owned pairing room |
| `GET` | `/t/:id/manifest` | Inspect encrypted metadata with a claim capability |
| `POST` | `/t/:id/claims` | Create an exclusive, short-lived claim lease |
| `GET` | `/t/:id/content` | Stream ciphertext, including byte ranges, with a claim token |
| `PATCH` | `/t/:id/claims/:claim_id` | Record downloaded bytes and renew a claim |
| `DELETE` | `/t/:id/claims/:claim_id` | Release a claim without consuming it |
| `POST` | `/t/:id/claims/:claim_id/commit` | Record verification and consume one download |
| `POST` | `/v1/identity-challenges` | Mint a one-use creation or rotation challenge |
| `POST`, `GET` | `/v1/identities` | Create or list agent identities |
| `GET`, `PATCH` | `/v1/identities/:id` | Read or disable an owned identity |
| `POST` | `/v1/identities/:id/keys` | Publish a predecessor-signed successor key |
| `POST` | `/v1/identities/:id/keys/:key_id/revoke` | Revoke a key for new use |
| `GET` | `/i/:id` | Read a bounded active public identity record |
| `GET` | `/i/:id/keys/:key_id` | Read one historical public key |
| `POST`, `GET` | `/v1/recipient-pins` | Upsert or list recipient pins |
| `DELETE` | `/v1/recipient-pins/:identity_id` | Idempotently remove a caller-owned recipient pin |
| `POST` | `/v1/transfers/:id/deliveries` | Prepare one addressed delivery |
| `GET` | `/v1/inbox` | Cursor-list deliveries for bound identities |
| `POST` | `/v1/inbox/:id/claims` | Claim an addressed delivery |
| `GET` | `/v1/inbox/:id/manifest` | Fetch an encrypted manifest with claim headers |
| `GET` | `/v1/inbox/:id/content` | Stream ciphertext with claim headers and ranges |
| `PATCH` | `/v1/inbox/:id/claims/:claim_id` | Report progress and renew the claim |
| `POST` | `/v1/inbox/:id/receipts` | Submit an idempotent signed receipt |
| `POST` | `/v1/inbox/:id/reject` | Submit a signed rejection receipt |
| `GET` | `/v1/deliveries/:id/receipts` | List sender-visible receipt history |
| `POST` | `/pair/:code/attempts` | Enter a pairing code with a one-use receiver key and nonce |
| `POST` | `/pair/attempts/:attempt_id/approve` | Approve the visible intent with the attempt capability |
| `GET` | `/pair/devices/:device_id` | Poll the room with the device capability |
| `POST` | `/pair/devices/:device_id/bind` | Bind one encrypted intent envelope to the approved receiver |
| `GET` | `/pair/attempts/:attempt_id` | Poll for the encrypted envelope with the attempt capability |

## Sealed asynchronous transfers

`aispace-sealed-v1` keeps the master key, filenames, content types, per-file sizes, and plaintext
hashes out of every request. `POST /v1/transfers` contains only aggregate declarations plus a
client-generated claim capability. The response returns independent upload and revoke
capabilities; idempotent create replay reproduces them from a server-held key while durable
storage contains only their SHA-256 verifiers.

Owner requests retain the bot bearer key and add `X-Upload-Capability` for part, manifest, and
complete operations or `X-Revoke-Capability` for deletion. Terminal writes and part uploads carry
`Idempotency-Key`. The encrypted manifest wire body is its random 12-byte AES-GCM nonce followed
by ciphertext and the 16-byte tag. The content object is the concatenation of independently
authenticated data chunks.

Public manifest and claim requests use the claim capability as their bearer. A successful claim
returns a separate lease token scoped to content reads, progress, release, and commit. A receiver
must send a final `PATCH` with the total `downloaded_bytes`, authenticate every chunk, verify each
file and the aggregate ciphertext digest, then commit the exact `manifest_sha256` and
`ciphertext_sha256`. Only commit consumes a finite download. Capability values and fragment
secrets must be redacted from logs and must never be placed in a query string.

V1 accepts at most 1,000 files, 10,000 ciphertext parts, and a 1 MiB encrypted manifest. Official
clients preflight those same limits before upload.

## Adaptive transport control plane

`POST /v1/transfers` optionally accepts `transport_mode: "adaptive"` with
`durability_policy: "durable_first"`. This still creates the R2 multipart upload immediately; live
negotiation cannot delay, replace, or weaken durable completion. `direct-first` and `live-only` are
not accepted in the current release.

The live-session schema accepts a 32-byte signaling capability only in the authenticated create
body; the service persists its SHA-256 verifier. Session bodies accept only bounded
capability identifiers and never ICE candidates or signaling payloads. Closing a session requires
the owning bot key and `X-Upload-Capability`. Privacy mode is fixed at creation, so `relay_only`
cannot later report a direct selection. Operational outcome counters are aggregated by UTC day,
privacy mode, path and outcome and contain no account, transfer, session, address, identity,
candidate, IP or filename fields.

The control-plane endpoints do not themselves provide WebRTC signaling or a byte path. Direct and
TURN kill switches remain disabled until those reviewed components exist. The current Go client
does not create live sessions; it records adaptive intent and immediately uses R2.

## Agent identity cryptography

Identity endpoints exchange unpadded base64url public keys and signatures;
private keys are never valid API fields. Content master keys are wrapped with
RFC 9180 base mode using DHKEM(X25519, HKDF-SHA256), HKDF-SHA256 and
AES-256-GCM. HPKE context binds the protocol, transfer ID, recipient identity
ID and exact recipient encryption-key version.

An addressed encrypted manifest carries a required `delivery` object binding
`mode: "addressed"`, `also_link`, `max_downloads`, recipient identity/key,
optional sender identity/signing key, transfer creation/expiry timestamps, file
count and declared plaintext bytes. Inbox receive compares those values, the
encrypted manifest digest and size, and the ciphertext digest/size against the
authenticated claim response before trusting a sender or emitting a receipt.

Signed manifests use Ed25519 over a domain-separated SHA-256 digest of RFC 8785
canonical JSON without the signature member. Receipts sign the domain plus the
canonical receipt directly and bind the delivery, transfer, recipient, signing
key, claim, server nonce, manifest digest, type and timestamp. Server
authorization controls inbox access; local fingerprint pins separately express
cryptographic trust.

Identity challenges, identity publication, key rotation/revocation, delivery
preparation and addressed inbox claims use `Idempotency-Key`. The CLI persists
the exact security-sensitive create/rotation/claim request state at mode `0600`
so a lost success response can be replayed without changing keys or losing the
claim nonce.

## Upload

Uploads are raw bodies rather than multipart forms:

```sh
curl -fsS -X POST https://aispace.sh/v1/files \
  -H "Authorization: Bearer $AISPACE_KEY" \
  -H "X-File-Name: report.pdf" \
  -H "Content-Type: application/pdf" \
  --data-binary @report.pdf
```

Optional headers are `X-Expires-In`, `X-SHA256`, and `X-File-Visibility`. Visibility may be
`private` or `account`; when omitted, the account's key-sharing setting applies. Account sharing
does not create a public URL. The server requires `Content-Length` and
returns the stored file metadata as JSON. It records a SHA-256 for every upload; `X-SHA256` is an
optional client-provided expectation that makes the server reject a body whose digest differs.

Public access always requires the separate, explicit link-creation endpoint below.

## Create a link

Creating public links requires Pro. Free accounts receive `402 payment_required`; existing links
continue through their original expiry if an account later downgrades.

```sh
curl -fsS -X POST https://aispace.sh/v1/files/FILE_ID/links \
  -H "Authorization: Bearer $AISPACE_KEY" \
  -H "Content-Type: application/json" \
  --data '{"expires_in":3600,"max_downloads":1}'
```

The creation response is the only response containing the complete share URL. Store it if needed;
the service retains only a token hash.

## Rate limits and quotas

`429` responses include `Retry-After`. The CLI retries one idempotent read at most once and does not
retry uploads, deletes, link creation, or monthly-cap failures. Effective limits are returned by
`GET /v1/quota`; clients should not hard-code plan values.

The quota response contains `key`, `account`, `month`, `limits`, and `rate`. The account-wide
`month` block reports upload/download usage and limits plus `period_end`, the next UTC reset.

For exact request and response types, see [`internal/api/types.go`](../internal/api/types.go),
[`internal/api/client.go`](../internal/api/client.go), and
[`internal/api/sealed.go`](../internal/api/sealed.go), which are the executable client contract.
