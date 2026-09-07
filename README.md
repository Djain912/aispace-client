<p align="center">
  <img src="assets/logo.svg" width="112" height="112" alt="aispace logo">
</p>

# aispace client

The open-source CLI and agent skill for [aispace.sh](https://aispace.sh), a bot-friendly file drop.
The hosted service is operated separately; this repository contains no server, billing, deployment,
or customer data. The client is built for humans and LLM agents: one binary, stable exit codes,
machine-readable JSON, and optional local age encryption.

> 🤖 **Using Grok?** [Add the aispace bot to Grok Bot →](https://x.ai/bot/suv5xSPPbQmzi02LF7Z9Z)

## Install

```sh
curl -fsSL https://aispace.sh/install.sh | sh
# npm
npm install -g @aispace-sh/cli
# Homebrew
brew install aispace-sh/tap/aispace
# pin a version
AISPACE_VERSION=v1.2.3 curl -fsSL https://aispace.sh/install.sh | sh
# or with Go
go install github.com/aispace-sh/aispace-client@latest
```

## Quickstart

```sh
aispace login --key ask_...                 # validates and saves ~/.config/aispace/config.json (0600)
aispace upload report.pdf --expires 7d --link --link-expires 1h --max-downloads 3
aispace upload notes.md                         # inherits account key-sharing setting
aispace upload secret.txt --private             # visible only to this key
echo "hello" | aispace upload - --name note.txt --link    # stdin; the URL is the last line
aispace upload secret.pdf --encrypt --identity-out secret.agekey --link
aispace keygen --identity-out receiver.agekey   # prints the public age1... recipient
aispace decrypt https://aispace.sh/d/... --identity-file secret.agekey --output secret.pdf
aispace ls                                  # all pages; --all accepted as a no-op
aispace info <file_id>
aispace download <file_id> --output ./file       # authenticated; no public link
aispace link <file_id> --expires 30m --max-downloads 1
aispace links <file_id>                     # list links (URLs are only shown at creation)
aispace revoke <link_id> [<link_id>...]
aispace rm <file_id> [<file_id>...]
aispace quota
aispace whoami
aispace version
aispace completion bash|zsh|fish
```

`upload` flags: `--name`, `--expires`, `--content-type` (default guessed from extension), `--sha256`
(sends `X-SHA256` for server-side verification), `--link`, `--link-expires`, `--max-downloads`,
`--private`, `--shared`, `--encrypt`, `--recipient`, and `--identity-out`.
Uploads stream from disk; stdin (`-`) is buffered to a temp file so `Content-Length` is known.
Encrypted uploads use age X25519 locally, spool ciphertext to a mode-0600 temp file, hash it, and
store it as `<name>.age`; the secret identity is never sent to the API.

With `--link` the share URL is always printed **last on its own line**, so an agent can take the last
line of stdout. With `--json` the output is exactly the API JSON; `upload --link --json` prints
`{"file": File, "link": ShareLink}` and `ls --json` prints `{"files": [...all pages], "next_cursor": null}`.
Encrypted upload JSON adds `"encryption"` with the public recipient and either a generated secret
identity or its saved path. Treat the identity as a credential.

## Configuration

Precedence: flag > environment > config file > default.

| Setting | Flag | Env | File key | Default |
|---|---|---|---|---|
| API key | `--key` | `AISPACE_KEY` | `key` | — |
| Server | `--url` | `AISPACE_URL` | `url` | `https://aispace.sh` |

`AISPACE_AGE_IDENTITY` supplies a decryption identity when `decrypt --identity-file` is omitted.
It is deliberately not accepted as a command-line value.

Config file: `$XDG_CONFIG_HOME/aispace/config.json` (default `~/.config/aispace/config.json`), written with
mode 0600. A warning is printed if the file is readable by others. `AISPACE_CONFIG` overrides the path.

Durations (`--expires`, `--link-expires`) accept Go syntax plus a `d` suffix: `30m`, `24h`, `7d`, `1d12h`,
or a bare number of seconds. Omitting them uses the server defaults (7 days for files, 1 hour for links).

## File permissions and links

File visibility controls authenticated key access. A public link is a separate capability: creating
one requires a Pro account and makes that one file available to anyone holding the URL.

| File mode | Who can access it? | File lifetime | Public-link lifetime | Download cap | Exposure if access leaks |
|---|---|---|---|---|---|
| `private` | Uploading key only | 7 days by default; maximum 7 days on Free or 30 days on Pro | None | Account monthly limit | Private files belonging to that key, until deletion or expiry |
| `account` | Every active key on the account | 7 days by default; maximum 7 days on Free or 30 days on Pro | None | Account monthly limit | Account-shared files, until deletion or expiry |
| `private` + public link | Uploading key and anyone with the URL | Maximum 30 days because links require Pro | 1 hour by default; maximum 30 days and never beyond file expiry | Optional per-link cap | Only the linked file, until link expiry, revocation, exhaustion, file deletion, or file expiry |
| `account` + public link | Account keys and anyone with the URL | Maximum 30 days because links require Pro | 1 hour by default; maximum 30 days and never beyond file expiry | Optional per-link cap | URL access ends with the link; account keys retain access until file deletion or expiry |
| Client-encrypted file | Visibility controls ciphertext access; only age identity holders can decrypt it | Same limits as the selected file mode | Same Pro-only limits when a link is created | Optional per-link cap | Plaintext exposure requires both the ciphertext and the age identity |

Available duration syntax includes `30s`, `15m`, `1h`, `36h`, and `7d`.

A file becomes unavailable when its file lifetime ends. A link can end sooner because it expired,
was revoked, or reached its download cap. Deleting the file immediately ends authenticated key
access and every associated public link.

## Exit codes

| Code | Meaning |
|---|---|
| 0 | success |
| 1 | generic error (network, 4xx/5xx not listed below) |
| 2 | usage error |
| 3 | authentication (401, or no key configured) |
| 4 | quota / size (402, 413) |
| 5 | rate limited (429) — idempotent GETs sleep `Retry-After` (max 30s) and retry once |

Errors go to stderr as `error: <message> (<code>)`; with `--json` they are a JSON object
`{"error":{"code","message","status","details","exit_code"}}` on stderr instead.

## Development

```sh
go test ./... && go vet ./... && test -z "$(gofmt -l .)"
go build -ldflags "-X main.version=0.0.0-dev" -o aispace .
```

Releases are cut through the manual GitHub Actions workflow; `.goreleaser.yaml` builds darwin/linux amd64/arm64 assets
and checksums, updates the Homebrew tap, and the release workflow publishes `@aispace-sh/cli` to npm.
See [`RELEASE.md`](RELEASE.md) for the one-time publisher configuration and release checklist.

## Agent skill

The reusable Codex-compatible skill lives in [`skills/aispace`](skills/aispace). Its instructions
keep uploads within user-authorized scope, prefer short-lived links, and treat encryption identities
as credentials.

## Security

Please report vulnerabilities privately through GitHub Security Advisories. Do not open a public
issue containing a credential, private link, or customer data. See [`docs/SECURITY.md`](docs/SECURITY.md).
