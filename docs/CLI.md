# aispace CLI reference

`aispace` is a single static Go binary. It wraps the bot API in [API.md](API.md) and is designed
to be driven by humans in a terminal and by LLM agents through a shell tool. Every command has a
`--json` mode that prints the API response verbatim so output can be parsed with `jq` without
guessing.

## Install

```sh
curl -fsSL https://aispace.sh/install.sh | sh
```

The installer detects OS/arch (darwin/linux, amd64/arm64), downloads the latest `v*` release
from `github.com/aispace-sh/aispace-client`, verifies the binary against `checksums.txt` (SHA-256) and
places it in `/usr/local/bin` (or `~/.local/bin` if that is not writable). Override with
`AISPACE_INSTALL_DIR=/path`. Manual install: download the asset for your platform from
[GitHub Releases](https://github.com/aispace-sh/aispace-client/releases), `chmod +x`, move to `$PATH`.

On Windows x64 or ARM64, install through npm; it downloads the matching checksummed `.exe` release:

```powershell
npm install -g @aispace-sh/cli
aispace version
```

Windows binaries are checksum-verified but not currently code-signed, so Windows may show a
SmartScreen warning on first run.

Build from source:

```sh
go build -o aispace . && ./aispace version
```

## Configuration

Resolution order (first match wins):

| Source | Key | URL |
|---|---|---|
| Flag | `--key` | `--url` |
| Environment | `AISPACE_KEY` | `AISPACE_URL` |
| Config file | `~/.config/aispace/config.json` → `key` | → `url` |
| Default | — | `https://aispace.sh` |

Config file (mode `0600`, directory `0700`), written by `aispace login`:

```json
{ "url": "https://aispace.sh", "key": "ask_XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX" }
```

`$XDG_CONFIG_HOME/aispace/config.json` is used when `XDG_CONFIG_HOME` is set. Ephemeral
environments (CI, agent sandboxes) can skip `login` entirely and export `AISPACE_KEY`.

| Env var | Purpose |
|---|---|
| `AISPACE_KEY` | Bot key; overrides the config file |
| `AISPACE_URL` | Base URL; overrides the config file (self-hosted or `http://localhost:8787`) |
| `AISPACE_AGE_IDENTITY` | Secret age X25519 identity used by `decrypt` when `--identity-file` is omitted |
| `AISPACE_CONFIG` | Full path to the config file, overriding the `XDG_CONFIG_HOME`/`HOME` lookup |
| `AISPACE_INSTALL_DIR` | Installer only: destination directory |

## Global flags

| Flag | Meaning |
|---|---|
| `--json` | Print the exact API JSON response and nothing else on stdout |
| `--key <ask_...>` | Override the key for this invocation |
| `--url <base>` | Override base URL for this invocation |
| `-h, --help` | Help |

`--json`, `--key` and `--url` are persistent flags, so they work on every subcommand — `--key` is
not limited to `login`.

Human-readable output goes to **stdout**; warnings go to **stderr**; errors go to **stderr** as a
single line `error: <message> (<code>)`:

```
error: Invalid key (invalid_key)
```

In `--json` mode stdout stays empty and the error is written to stderr as one object that wraps
the API's code and message together with the exit code, so a caller can branch on either:

```json
{"error":{"code":"invalid_key","exit_code":3,"message":"Invalid key","status":401}}
```

`details` and `retry_after` are included when the server supplies them.

## Exit codes

| Code | Meaning | Typical cause |
|---|---|---|
| `0` | Success | |
| `1` | Generic failure | Network error, 5xx, unexpected response, file not found locally |
| `2` | Usage error | Bad flag, missing argument, unparsable duration, control character in the key, `--name` or `--content-type` |
| `3` | Authentication | No key configured, `401 invalid_key`, `401 key_revoked` |
| `4` | Quota | `402 quota_exceeded`, `402 allowance_exceeded`, `402 payment_required`, `400` or `413 file_too_large` |
| `5` | Rate limited or monthly cap | `429 rate_limited` after the retry policy below gave up; `429 monthly_upload_cap` / `429 monthly_download_cap` (never retried — the reset is next month) |

Retry policy: the CLI retries **once** on either of two conditions:

- `429 rate_limited` for idempotent `GET`s (`ls`, `quota`, `download` and metadata lookups), waiting
  the `Retry-After` the server gives (capped at 30s).
- `502`, `503` or `504` for non-metered metadata `GET`s. The CLI does not replay authenticated
  downloads after a gateway error because each request consumes monthly download allowance and the
  first attempt may already have been counted.

`500` is **not** retried: it usually means the request itself is the problem, so a second attempt
doubles the load and returns the same error. Writes are never retried either — a `503` may still
have been applied, and repeating an upload could store the file twice. `monthly_upload_cap` and
`monthly_download_cap` are never retried — `Retry-After` points at the start of the next UTC month
— and exit `5` is returned immediately with the server's message (which points at
contacts@aispace.sh). Uploads, link creation and deletes are never
retried automatically; the agent decides. `Retry-After` is echoed in the error message.

## Timeouts

There is **no deadline on a transfer as a whole**. A large file over a slow link is slow, not
broken, and a single cap covering the response body would really be a bandwidth floor: at a
ten-minute cap, a 100 MB upload has to sustain about 1.4 Mbit/s or fail after moving most of
itself.

What is bounded are the phases before any bytes flow, so an unreachable or silent server is still
given up on quickly:

| Phase | Limit |
|---|---|
| TCP connect | 30s |
| TLS handshake | 10s |
| Waiting for response headers | 60s |

A stalled transfer is therefore ended by the peer or by the operator, not by a timer:
`Ctrl-C`/`SIGTERM` cancels in-flight requests and exits `1` with code `interrupted`.

## Durations

Flags that take a duration accept Go-style strings with an added `d` unit: `30s`, `15m`, `1h`,
`36h`, `7d`. Plain integers are seconds. File lifetimes above the plan maximum are rejected;
links are clamped to their plan maximum and never outlive their file. Both cap at 7d on Free and
30d on Paid, and the CLI prints the effective expiry returned by the server.

## Commands

### `aispace login`

```
aispace login --key ask_... [--url https://aispace.sh]
```

Validates the key with `GET /v1/whoami`, then writes the config file. Prints the key's name,
prefix and the account email.

```sh
$ aispace login --key ask_9fK2mQ1xAbCdEfGhIjKlMnOpQrStUvWx
logged in as key "research-bot" (ask_9fK2mQ1x) for luigi@example.com
saved /home/you/.config/aispace/config.json
```

The URL is only written to the config file when it differs from the default `https://aispace.sh`.

| Exit | When |
|---|---|
| 3 | key rejected |
| 2 | `--key` missing (and `AISPACE_KEY` unset), or the key does not start with `ask_` |

### `aispace upload`

```
aispace upload <path|-> [--name N] [--expires 7d] [--link] [--link-expires 1h]
                        [--max-downloads N] [--content-type T] [--sha256]
                        [--private | --shared]
                        [--encrypt] [--recipient age1...] [--identity-out PATH] [--json]
```

Uploads one file with `POST /v1/files` (raw streamed body). `-` reads stdin.

| Flag | Default | Notes |
|---|---|---|
| `--name N` | basename of `<path>`, or `stdin` for `-` | Sent as `X-File-Name` |
| `--expires D` | server default (7d) | File lifetime, `X-Expires-In`; max 30d |
| `--link` | off | After upload, also create a Pro public share link and print its URL |
| `--link-expires D` | server default (1h) | Link lifetime; **requires** `--link`, exit 2 without it |
| `--max-downloads N` | unlimited | Link download cap; **requires** `--link`, exit 2 without it |
| `--content-type T` | sniffed from extension, else `application/octet-stream` | Stored type |
| `--sha256` | off | Compute SHA-256 locally and send `X-SHA256` so R2 verifies the body |
| `--private` | account setting | Restrict this upload to the current key |
| `--shared` | account setting | Make this upload readable by every key on the account |
| `--encrypt` | off | Encrypt locally using age X25519; the service receives ciphertext only |
| `--recipient age1...` | generated one-time recipient | Encrypt to an identity already held by the recipient |
| `--identity-out PATH` | print once on stdout/JSON | Save a generated identity to a new mode-`0600` file; never overwrites |

Stdin is spooled to a temporary file first because the API requires `Content-Length`; the temp
file is removed after the request. There is no size limit on the CLI side; the server enforces
`max_file_bytes` (5 MB on Free, 100 MB on Pro) and returns exit 4 if exceeded. Check
`aispace quota --json | jq .limits.max_file_bytes` before large uploads.

When neither `--private` nor `--shared` is supplied, the server applies the account's “Share files
between my keys” setting, which is enabled by default. Account sharing is authenticated and does
not create a public URL. `--link` is a separate, explicit public-sharing action available on Pro.

```sh
# file
aispace upload ./report.pdf --expires 3d

# stdin from a pipeline
some-command | aispace upload - --name output.log

# upload + link in one go
echo "hello" | aispace upload - --name hello.txt --link --link-expires 1h --max-downloads 3

# end-to-end encrypted; keep secret.agekey and send it separately from the URL
aispace upload secret.pdf --encrypt --identity-out secret.agekey --link --max-downloads 1
```

Encrypted uploads are written to an owner-only temporary file first so their encrypted
`Content-Length` and SHA-256 are known. The stored name gains `.age`, the content type is
`application/vnd.aispace.age`, and the `File.enc_alg` field is `age-x25519`. If `--recipient` is
not supplied, the command generates a one-time identity. Losing that identity makes the file
unrecoverable. Anyone who has both ciphertext and the identity can decrypt it even after a link
is revoked, so deliver them through separate channels when practical.

Human output (one line per object). With `--link` the URL is printed **last, on its own line**, so
an agent can take the final line of stdout:

```
uploaded 01J8ZQ3V9N7X2K4M6P8R0T2W4Y hello.txt 6 B expires 2026-09-12T17:00:00Z
link 01J8ZQ5C1D3F5H7J9L1N3P5R7T expires 2026-09-05T18:00:00Z max_downloads 3
https://aispace.sh/d/K3f9Qm2xP7vL1nR8sT4wY6zA0bC5dE9fG3hJ7kM2nP4
```

Timestamps are RFC 3339 in UTC, and `max_downloads` is `unlimited` when no cap was set.

`--json` output: the `File` object; with `--link`, an envelope `{ "file": File, "link": ShareLink }`
so both IDs and the URL are available in one parse. Encrypted uploads always return an envelope
with an additional `encryption` object. It contains `algorithm`, `recipient`, `original_name`, and
either the one-time secret `identity` or `identity_file`. Treat `identity` as a credential.

```sh
url=$(echo "$report" | aispace upload - --name report.md --link --link-expires 2h --json | jq -r .link.url)
file_id=$(aispace upload big.zip --json | jq -r .id)
```

Exit codes: 4 on any quota/size rejection, 5 on rate limit (uploads are never retried), 3 on bad
key, 2 for a usage error such as `--link-expires` without `--link` or pointing at a directory. A
path that does not exist is exit 1.

If the upload succeeds but the link cannot be created, the file line (and, for `--encrypt`, the
encryption block) is still printed before the error, so the stored file is not lost.

If the server *rejects* the upload (quota, size, rate limit, bad key), a file written by
`--identity-out` is removed again: nothing was stored, so that identity decrypts nothing, and
leaving it behind would make retrying the same command fail with `identity file already exists`.
When the request fails without a response or returns a 5xx server error, the outcome is uncertain,
so the identity is kept and a warning names it — check `aispace ls` before deleting it, because the
file may have been stored.

### `aispace download`

```
aispace download <file_id> --output <path|-> [--verify] [--json]
```

Downloads a file owned by the current key or shared with the account by another key. This uses
key authentication and does not create or require a public link. Destination files are mode
`0600`, never overwritten, and removed if the transfer fails. Use `--output -` for raw stdout;
it cannot be combined with `--json`.

```sh
aispace download 01J8ZQ3V9N7X2K4M6P8R0T2W4Y --output report.pdf
aispace download 01J8ZQ3V9N7X2K4M6P8R0T2W4Y --output report.pdf --verify
```

`--verify` hashes the bytes as they are written and compares the result with the SHA-256 the API
records for every uploaded file, so a truncated or altered transfer is caught rather than trusted.
It reads the file's metadata first, which costs **one extra request** against the per-minute rate
limit. An incomplete or incompatible server response without a digest exits 1 with `no_checksum`
rather than reporting a check it did not perform.

On a mismatch the exit code is 1 with `checksum_mismatch`, and the output file is removed, so a
caller that ignores the exit code cannot pick up a corrupt file. With `--output -` the bytes have
already gone to stdout by the time the digest is known; the mismatch is still reported and the
exit code is still 1, but the caller has to discard what it read.

```sh
aispace download "$id" --output report.pdf --verify || echo "corrupt, nothing written"

# --json adds the digest that was verified
aispace download "$id" --output report.pdf --verify --json | jq -r .sha256
```

### `aispace keygen`

```
aispace keygen [--identity-out PATH] [--json]
```

Generates an age X25519 identity and public recipient entirely locally. It does not require an
aispace account or make a network request. Without `--identity-out`, the secret identity is printed
to stdout. With `--identity-out`, the identity is written to a new mode-`0600` file and never
printed; an existing file is not overwritten.

```sh
aispace keygen --identity-out receiver.agekey
# recipient age1...
# identity saved receiver.agekey

aispace keygen --identity-out receiver.agekey --json
# {"algorithm":"age-x25519","recipient":"age1...","identity_file":"receiver.agekey"}
```

Give the `age1...` recipient to a sender, who can encrypt for it with
`aispace upload --encrypt --recipient age1...`. Keep the `AGE-SECRET-KEY-...` identity private.

### `aispace decrypt`

```
aispace decrypt <path|url|-> --output <path|-> [--identity-file PATH] [--json]
```

Downloads if necessary and decrypts locally. The identity is read from `--identity-file`, or from
`AISPACE_AGE_IDENTITY`; there is deliberately no identity value flag, to keep secrets out of shell
history and process listings. Local `.age` input defaults to the same path without `.age` when
`--output` is omitted; any other local input falls back to `<input>.decrypted`, and the command
refuses to write over its own input. URL and stdin inputs require `--output`.

Output files use mode `0600`, are never overwritten, and are deleted if age authentication fails.
`--output -` streams plaintext to stdout and cannot be combined with `--json`.

```sh
aispace decrypt report.pdf.age --identity-file report.agekey --output report.pdf
AISPACE_AGE_IDENTITY='AGE-SECRET-KEY-...' aispace decrypt 'https://aispace.sh/d/...' -o report.pdf
```

### `aispace link`

```
aispace link <file_id> [--expires 1h] [--max-downloads N] [--json]
```

Creates a new share link for an existing file (`POST /v1/files/:id/links`). Multiple links per
file are fine; each has its own expiry, cap and counter, and can be revoked independently.

```sh
aispace link 01J8ZQ3V9N7X2K4M6P8R0T2W4Y --expires 24h --max-downloads 1
# → link 01J8ZQ5C1D3F5H7J9L1N3P5R7T expires 2026-09-06T17:00:00Z max_downloads 1
# → https://aispace.sh/d/...          (last line, so `| tail -n1` is the URL)

aispace link 01J8ZQ3V9N7X2K4M6P8R0T2W4Y --json | jq -r '.url, .expires_at'
```

Exit 1 with `not_found` if the file is unknown to this key or already expired.

### `aispace ls`

```
aispace ls [--all] [--json]
```

Lists this key's live files (`GET /v1/files`). It **always** follows `next_cursor` to the end, in
both human and `--json` mode. `--all` is accepted for compatibility and does nothing.

Human output is one line per file, `<id> <size> <expires> <name>`, with no header:

```
01J8ZQ3V9N7X2K4M6P8R0T2W4Y 1.0 MB 2026-09-12T17:00:00Z report.pdf
01J8ZQ5C1D3F5H7J9L1N3P5R7T 6 B 2026-09-12T17:01:00Z hello.txt
```

When the key holds no files, nothing is written to stdout and `no files` goes to stderr, so a
`--json`-free pipeline stays empty. `--json` prints every page merged into one object, with
`next_cursor` always `null` because the walk is already finished:

```sh
# total bytes held by this key
aispace ls --json | jq '[.files[].size_bytes] | add'

# files expiring within 24 h
aispace ls --json | jq -r --argjson t "$(date +%s)" '.files[] | select(.expires_at - $t < 86400) | .name'
```

### `aispace rm`

```
aispace rm <file_id> [<file_id>...] [--continue] [--json]
```

Deletes files (`DELETE /v1/files/:id`). All share links of the file stop working immediately and
the key's used bytes drop. Prints `deleted <id>` per file; with `--json`, one object **per line**
(JSON Lines, not one array):

```
{"deleted":"01J8ZQ3V9N7X2K4M6P8R0T2W4Y"}
{"deleted":"01J8ZQ5C1D3F5H7J9L1N3P5R7T"}
```

IDs are processed in order and the command **stops at the first failure**, so the IDs after it are
not deleted; the ones already printed were. The exit code is the one for the underlying error (1
for `not_found`, 3 for a bad key), and when more than one ID was given the message is prefixed
with the ID that failed.

`--continue` attempts every ID instead. Failures are reported to stderr as they happen, successes
still go to stdout, and the command exits non-zero if any ID failed — so a cleanup pass over a list
that may contain already-deleted or expired IDs completes in one call:

```sh
aispace ls --json | jq -r '.files[].id' | xargs aispace rm --continue
```

A failure that applies to every remaining ID still stops the run: a rejected key (`401`) or an
exhausted rate limit (`429`) would fail for all of them, so `--continue` gives up rather than
spending the rest of the batch on requests that cannot succeed. A missing ID is not treated that
way, because it says nothing about the others.

With `--json` the two streams stay separate: stdout carries only `{"deleted": id}` lines, and each
failure is a JSON error object on stderr, so a caller parsing stdout is never handed a mixed
stream.

### `aispace revoke`

```
aispace revoke <link_id> [<link_id>...] [--continue] [--json]
```

Revokes share links (`DELETE /v1/links/:id`). The file remains. Link IDs come from
`upload --link --json`, `link --json`, or `aispace links <file_id>`.

Like `rm`, it prints `revoked <id>` per link (or `{"revoked":"<id>"}` per line with `--json`),
processes IDs in order, stops at the first failure, and accepts `--continue` to attempt every ID
instead.

### `aispace links`

```
aispace links <file_id> [--json]
```

Lists the links of a file (`GET /v1/files/:id/links`) with `download_count`, `max_downloads`,
`expires_at`, `revoked_at`. URLs are not shown (the server does not keep them).

### `aispace info`

```
aispace info <file_id> [--json]
```

File metadata (`GET /v1/files/:id`).

### `aispace quota`

```
aispace quota [--json]
```

Five lines, one per group:

```
key: used 1.0 MB of 50.0 MB, 49.0 MB remaining
account: used 3.0 MB of 100.0 MB, 97.0 MB remaining, plan free (extra blocks 0)
month: uploads 12/100, downloads 340/1000, resets 2025-10-01T00:00:00Z
limits: max file 25.0 MB, max file ttl 30d, max link ttl 7d, uploads 60/h 500/d, requests 300/min
rate: uploads remaining 58 this hour, 490 today; requests remaining 299 this minute
```

The typed response is `key`, `account`, `month`, `limits` and `rate` — see
[`internal/api/types.go`](../internal/api/types.go). Monthly counters are account-wide and reset at
`month.period_end`. Exhaustion surfaces as `429 monthly_upload_cap` or
`429 monthly_download_cap` and is never retried automatically.

```sh
aispace quota --json | jq '.key.remaining_bytes'
aispace quota --json | jq -e '.rate.uploads_hour_remaining > 0' >/dev/null || echo "wait"
aispace quota --json | jq '.month | {uploads_left: (.uploads_limit - .uploads_used), downloads_left: (.downloads_limit - .downloads_used), period_end}'

# largest file this account may upload right now
aispace quota --json | jq '[.limits.max_file_bytes, .account.remaining_bytes] | min'
```

### `aispace whoami`

Prints key name, prefix, account email and the key's usage against its budget
(`GET /v1/whoami`):

```
research-bot (ask_9fK2mQ1x) luigi@example.com used 1.0 MB of 50.0 MB
```

Exit 3 if the key is invalid, and also if the server reports the key as revoked.

### `aispace version`

```
aispace 0.3.1 (darwin/arm64)
```

The version string is injected at build time with `-ldflags "-X main.version=..."` and is `dev` in
an unstamped local build. `--json` prints the version and the exact `User-Agent` the client sends:

```json
{"user_agent":"aispace-cli/0.3.1 (darwin/arm64)","version":"0.3.1"}
```

## Recipes

Upload a directory as a tarball with a single-download link:

```sh
tar czf - ./out | aispace upload - --name out.tgz --expires 1d --link --link-expires 6h --max-downloads 1
```

Script-safe upload with error handling:

```sh
if out=$(aispace upload result.csv --link --json 2>err.txt); then
  echo "share: $(jq -r .link.url <<<"$out")"
else
  case $? in
    4) echo "quota exceeded: $(cat err.txt)"; aispace quota ;;
    5) echo "rate limited: $(cat err.txt)"; sleep 60 ;;
    3) echo "bad key" ;;
    *) echo "failed: $(cat err.txt)" ;;
  esac
fi
```

Rotate to a new key without downtime: create the new key in the dashboard, run
`aispace login --key ask_new...` (config is overwritten atomically), then revoke the old key. Files
uploaded under the old key stay until their expiry and their links keep working; they are only
listable via the dashboard, not via `ls` under the new key.

Point at a local dev server:

```sh
AISPACE_URL=http://localhost:8787 AISPACE_KEY=ask_dev... aispace quota
```

## Shell completion

```sh
aispace completion bash > /etc/bash_completion.d/aispace
aispace completion zsh > "${fpath[1]}/_aispace"
aispace completion fish > ~/.config/fish/completions/aispace.fish
```
