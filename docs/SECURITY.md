# Client security

This repository contains the open-source `aispace` CLI and agent skill. The hosted service and its
operational configuration are maintained separately.

## Credentials

- Bot keys use the form `ask_...`. The CLI sends them only in the `Authorization` header to the configured server.
- `aispace login` stores configuration with mode `0600` in a mode-`0700` directory.
- Prefer `AISPACE_KEY` in ephemeral environments. Never commit keys or place them in command output, issues, or prompts.
- Share URLs are bearer credentials. Anyone holding a live URL can download its file.

## Client-side encryption

`aispace upload --encrypt` encrypts locally with age X25519 before upload. The service receives
ciphertext, not plaintext or the private identity. Generated identity files use mode `0600` and
are never overwritten.

Treat `AGE-SECRET-KEY-...` identities as credentials. Never upload them with their ciphertext.
Deliver the URL and identity through separate authenticated channels when practical. Encryption
protects confidentiality and ciphertext integrity; it does not prove who sent the file.

## Network boundary

The default API origin is `https://aispace.sh`. `AISPACE_URL` and `--url` can select another
deployment. Review that origin before sending credentials or files. The client rejects malformed
URLs and redacts credentials from errors and diagnostic output.

## Reporting vulnerabilities

Use the repository's private GitHub Security Advisory flow. Include affected versions and a minimal
reproduction, but do not include active bot keys, share links, private identities, or customer data.
