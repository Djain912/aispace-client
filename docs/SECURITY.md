# Client security

This repository contains the open-source `aispace` CLI and agent skill. The hosted service and its
operational configuration are maintained separately.

## Credentials

- Bot keys use the form `ask_...`. The CLI sends them only in the `Authorization` header to the configured server.
- `aispace login` stores configuration with mode `0600` in a mode-`0700` directory.
- Prefer `AISPACE_KEY` in ephemeral environments. Never commit keys or place them in command output, issues, or prompts.
- Legacy share URLs and complete sealed fragment links are bearer credentials. Anyone holding one
  can use its scoped authority while it remains live.
- Mode-`0600` sealed owner tickets, identity private records, and recipient tokens are separate
  credentials. Do not substitute one for another or store them in ordinary logs.

## Client-side encryption

`aispace upload --encrypt` encrypts locally with age X25519 before upload. The service receives
ciphertext, not plaintext or the private identity. Generated identity files use mode `0600` and
are never overwritten. When an identity would normally be printed after upload, the CLI holds a
mode-`0600` recovery copy in a private directory beside its configuration file until the server
outcome is known. It retains and names that file only if a network or server failure leaves it
uncertain whether ciphertext was stored. After abrupt process termination, inspect `recovery/`
beside `config.json` before removing leftover identities.

Treat `AGE-SECRET-KEY-...` identities as credentials. Never upload them with their ciphertext.
Deliver the URL and identity through separate authenticated channels when practical. Encryption
protects confidentiality and ciphertext integrity; it does not prove who sent the file.

## Network boundary

The default API origin is `https://aispace.sh`. `AISPACE_URL` and `--url` can select another
deployment. Review that origin before sending credentials or files. HTTPS is required except for
loopback development origins. Sealed tokens, owner tickets, identity records, recipient pins, and
pairing summaries are origin-bound and fail closed on a different configured service. The client
rejects malformed URLs and redacts credentials from errors and diagnostic output.

## Sealed delivery, identity, and handoff

`aispace transfer create` encrypts filenames, per-file metadata, and content locally. The service
stores ciphertext and bounded aggregate policy fields, never the master key or private manifest.
Losing the recipient key can make a transfer unrecoverable; losing the owner ticket removes the
CLI resume and revoke path. See [Secure handoffs](SECURE_HANDOFFS.md) for recovery steps.

Identity private keys remain CLI-held and have no server-side recovery. Bot-key authorization and
identity bindings control inbox access, while local signature verification and fingerprint pins
control sender trust. A valid signature from an unpinned key is not a trusted sender.

Five-minute pairing codes are rendezvous locators, not decryption keys. Current pairing binds one
approved one-use receiver key; QR generation and PAKE are not implemented. Adaptive mode has no
WebRTC/TURN/native byte driver and continues over durable R2.

## Reporting vulnerabilities

Use the repository's private GitHub Security Advisory flow. Include affected versions and a minimal
reproduction, but do not include active bot keys, share links or transfer tokens, private
identities, owner tickets, or customer data.
