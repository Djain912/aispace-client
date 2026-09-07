---
name: aispace
description: Store, exchange, securely share, receive, and manage temporary files with the aispace CLI. Use for authenticated file exchange between keys on one account, public handoffs through expiring links, optional client-side age encryption, quota inspection, or revoking and deleting prior shares.
---

# aispace client

Use the `aispace` CLI to exchange temporary artifacts. Keep all actions within the files, keys,
and recipients the user placed in scope.

## Check the client

1. Run `aispace version` when availability is uncertain.
2. Run `aispace whoami` before the first remote action. If authentication fails, ask the user to
   configure `AISPACE_KEY` or run `aispace login`; never request that they paste the key in chat.
3. Before a large file or batch, inspect `aispace quota --json`. Respect file-size, byte-budget,
   monthly-count, and rate limits. Do not retry quota or monthly-cap failures.

## Store files for this account

Upload without `--link` unless the user asks for a public share URL:

```sh
aispace upload PATH --json
```

For stdin, always supply a meaningful name:

```sh
producer | aispace upload - --name result.json --json
```

Uploads inherit the account's key-sharing setting, which is enabled by default. Use `--private`
when the file must remain visible only to the current key, or `--shared` to explicitly make it
readable by sibling keys. Neither mode exposes the file publicly.

For another agent on the same account, pass the file ID. That agent can find shared files with
`aispace ls --json` and download one without a public URL:

```sh
aispace download FILE_ID --output PATH
```

## Create a public share

Only mint a public link when the user asks to share the file outside the account or explicitly
requests a link. Public links require Pro; check `.account.plan` with `aispace quota --json` first.
If the account is Free, explain that account-internal sharing still works and ask the user to move
to Pro rather than uploading and then failing link creation.

```sh
aispace upload PATH --link --link-expires 1h --json
```

Read `.link.url`, `.link.expires_at`, `.file.id`, and `.link.id` from JSON. Tell the user when the
link expires and any download cap. Prefer a one-hour link; use `--max-downloads 1` for a deliberate
one-recipient transfer. Mint a distinct link for each recipient.

Do not upload secrets, credentials, private keys, or identity files as ordinary artifacts.

## Encrypt a handoff

Use client encryption when the user requests it or the artifact contains sensitive material that
the storage operator should not see. For account storage or a sibling-key handoff, do not create a
public link:

```sh
aispace upload PATH --encrypt --identity-out PATH.agekey --json
```

The output file stored by aispace is age X25519 ciphertext. The identity file is created locally
with mode `0600`; aispace never receives it. Treat `AGE-SECRET-KEY-...` as a credential:

- Never upload it to aispace, commit it, include it in a prompt, or expose it in logs.
- When using a public link, deliver the share URL and identity through separate authenticated
  channels when practical.
- Explain that losing the identity makes the ciphertext unrecoverable.
- Explain that anyone holding both ciphertext and identity can decrypt even after link revocation.
- Do not claim encryption proves authorship. It authenticates ciphertext integrity, not the sender.

If the receiving bot already supplied an `age1...` public recipient, prefer:

```sh
aispace upload PATH --encrypt --recipient 'age1...' --shared --json
```

Only the receiving bot retains the corresponding identity. Do not also pass `--identity-out`.

Generate a recipient and save its identity locally when the receiver does not already have one:

```sh
aispace keygen --identity-out PATH.agekey --json
```

Share only `.recipient`; keep the identity file private.

## Receive and decrypt

Keep the identity in a local file and decrypt without sending it to the service:

```sh
aispace decrypt 'https://aispace.sh/d/...' --identity-file PATH.agekey --output OUTPUT --json
```

The output path must not already exist. If authentication fails, stop and report corrupted data or
the wrong identity; do not use partial plaintext. Open or validate the output before reporting a
successful handoff. Do not delete the encrypted source or identity unless the user explicitly asks
and the durable destination has been verified.

## Manage existing shares

- List this key's files and account-shared files: `aispace ls --json`
- Download an accessible file without a public link: `aispace download FILE_ID --output PATH`
- Inspect one file, including `enc_alg`: `aispace info FILE_ID --json`

Only the key that uploaded a file may manage it or its public links. Account-shared sibling keys
can list, inspect, and download the file, but cannot perform these owner-only operations:

- Mint another link without re-uploading: `aispace link FILE_ID --expires 1h --json`
- List link IDs and counters: `aispace links FILE_ID --json`
- Revoke a link created by this key: `aispace revoke LINK_ID`
- Delete a file uploaded by this key and invalidate all its links: `aispace rm FILE_ID`

Treat revoke and delete as state-changing actions. Do them only when requested or when they are an
explicit step in the user's stated workflow. Remember that expiry or deletion cannot recall bytes
that a recipient already downloaded.

## Report the result

Return the file ID and whether client encryption was used. When a public link was requested and
created, also return its URL, effective expiration, and download cap. Return the local identity-file
path when one was created. Never reproduce the identity itself unless the user explicitly asks to
transfer that secret in the current channel.
