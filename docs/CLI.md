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

Build from source:

```sh
go build -o aispace . && ./aispace version
```

## Configuration

Resolution order (first match wins):

| Source | Key | URL |
|---|---|---|
| Flag | `--key` (login only) | `--url` |
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
| `AISPACE_INSTALL_DIR` | Installer only: destination directory |
| `NO_COLOR` | Disable ANSI colour in human output |

## Global flags

| Flag | Meaning |
|---|---|
| `--json` | Print the exact API JSON response (or `{}` for `204`) and nothing else on stdout |
| `--url <base>` | Override base URL for this invocation |
| `-q, --quiet` | Suppress human-readable progress on stderr |
| `-h, --help` | Help |

Human-readable output goes to **stdout**; progress and warnings go to **stderr**; errors go to
**stderr** as a single line `error: <code>: <message>`. In `--json` mode errors are printed to
stderr as the API's JSON error object, and stdout stays empty.

## Exit codes

| Code | Meaning | Typical cause |
|---|---|---|
| `0` | Success | |
| `1` | Generic failure | Network error, 5xx, unexpected response, file not found locally |
| `2` | Usage error | Bad flag, missing argument, unparsable duration |
| `3` | Authentication | No key configured, `401 invalid_key`, `401 key_revoked` |
| `4` | Quota | `402 quota_exceeded`, `402 allowance_exceeded`, `402 payment_required`, `400/413 file_too_large` |
| `5` | Rate limited or monthly cap | `429 rate_limited` after the retry policy below gave up; `429 monthly_upload_cap` / `429 monthly_download_cap` (never retried — the reset is next month) |

Retry policy: on `429 rate_limited` the CLI reads `Retry-After` and retries **once**, only for
idempotent `GET`s (`ls`, `quota`, and the metadata lookups). `monthly_upload_cap` and
`monthly_download_cap` are never retried — `Retry-After` points at the start of the next UTC month
— and exit `5` is returned immediately with the server's message (which points at
contacts@aispace.sh). Uploads, link creation and deletes are never
retried automatically; the agent decides. `Retry-After` is echoed in the error message.

## Durations

Flags that take a duration accept Go-style strings with an added `d` unit: `30s`, `15m`, `1h`,
`36h`, `7d`. Plain integers are seconds. The server clamps values to its maxima
(files and links both cap at 7d on Free and 30d on Paid, and a link never outlives its file) and the CLI prints the effective value it got back.

## Commands

### `aispace login`

```
aispace login --key ask_... [--url https://aispace.sh]
```

Validates the key with `GET /v1/whoami`, then writes the config file. Prints the key's name,
prefix and the account email.

```sh
$ aispace login --key ask_9fK2mQ1xAbCdEfGhIjKlMnOpQrStUvWx
logged in as research-bot (ask_9fK2mQ1x…) for luigi@example.com at https://aispace.sh
```

| Exit | When |
|---|---|
| 3 | key rejected |
| 2 | `--key` missing or not of the form `ask_` + 32 base62 chars |

### `aispace upload`

```
aispace upload <path|-> [--name N] [--expires 7d] [--link] [--link-expires 1h]
                        [--max-downloads N] [--content-type T] [--sha256]
                        [--encrypt] [--recipient age1...] [--identity-out PATH] [--json]
```

Uploads one file with `POST /v1/files` (raw streamed body). `-` reads stdin.

| Flag | Default | Notes |
|---|---|---|
| `--name N` | basename of `<path>`; **required** with `-` | Sent as `X-File-Name` |
| `--expires D` | server default (7d) | File lifetime, `X-Expires-In`; max 30d |
| `--link` | off | After upload, also create a share link and print its URL |
| `--link-expires D` | 1h | Link lifetime (implies `--link`) |
| `--max-downloads N` | unlimited | Link download cap (implies `--link`) |
| `--content-type T` | sniffed from extension, else `application/octet-stream` | Stored type |
| `--sha256` | off | Compute SHA-256 locally and send `X-SHA256` so R2 verifies the body |
| `--encrypt` | off | Encrypt locally using age X25519; the service receives ciphertext only |
| `--recipient age1...` | generated one-time recipient | Encrypt to an identity already held by the recipient |
| `--identity-out PATH` | print once on stdout/JSON | Save a generated identity to a new mode-`0600` file; never overwrites |

Stdin is spooled to a temporary file first because the API requires `Content-Length`; the temp
file is removed after the request. There is no size limit on the CLI side; the server enforces
`max_file_bytes` (5 MB on Free, 100 MB on Pro) and returns exit 4 if exceeded. Check
`aispace quota --json | jq .limits.max_file_bytes` before large uploads.

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

Human output (one line per object):

```
uploaded hello.txt (6 B) id=01J8ZQ3V9N7X2K4M6P8R0T2W4Y expires 2026-09-12T17:00:00Z
link https://aispace.sh/d/K3f9Qm2xP7vL1nR8sT4wY6zA0bC5dE9fG3hJ7kM2nP4 expires 2026-09-05T18:00:00Z max_downloads=3
```

`--json` output: the `File` object; with `--link`, an envelope `{ "file": File, "link": ShareLink }`
so both IDs and the URL are available in one parse. Encrypted uploads always return an envelope
with an additional `encryption` object. It contains `algorithm`, `recipient`, `original_name`, and
either the one-time secret `identity` or `identity_file`. Treat `identity` as a credential.

```sh
url=$(echo "$report" | aispace upload - --name report.md --link --link-expires 2h --json | jq -r .link.url)
file_id=$(aispace upload big.zip --json | jq -r .id)
```

Exit codes: 4 on any quota/size rejection, 5 on rate limit (not retried), 3 on bad key, 2 if `-`
without `--name` or the path does not exist.

### `aispace decrypt`

```
aispace decrypt <path|url|-> --output <path|-> [--identity-file PATH] [--json]
```

Downloads if necessary and decrypts locally. The identity is read from `--identity-file`, or from
`AISPACE_AGE_IDENTITY`; there is deliberately no identity value flag, to keep secrets out of shell
history and process listings. Local `.age` input defaults to the same path without `.age` when
`--output` is omitted. URL and stdin inputs require `--output`.

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
# → link https://aispace.sh/d/... expires 2026-09-06T17:00:00Z max_downloads=1

aispace link 01J8ZQ3V9N7X2K4M6P8R0T2W4Y --json | jq -r '.url, .expires_at'
```

Exit 1 with `not_found` if the file is unknown to this key or already expired.

### `aispace ls`

```
aispace ls [--all] [--json]
```

Lists this key's live files (`GET /v1/files`). Follows `next_cursor` to the end unless `--json`
is used, in which case the raw first page is printed; use `--all --json` to get a merged
`{ "files": [...], "next_cursor": null }` across all pages.

```
ID                          SIZE      EXPIRES               NAME
01J8ZQ3V9N7X2K4M6P8R0T2W4Y  1.0 MB    2026-09-12 17:00 UTC  report.pdf
01J8ZQ5C1D3F5H7J9L1N3P5R7T  6 B       2026-09-12 17:01 UTC  hello.txt
```

```sh
# total bytes held by this key
aispace ls --all --json | jq '[.files[].size_bytes] | add'

# files expiring within 24 h
aispace ls --all --json | jq -r --argjson t "$(date +%s)" '.files[] | select(.expires_at - $t < 86400) | .name'
```

### `aispace rm`

```
aispace rm <file_id> [<file_id>...] [--json]
```

Deletes files (`DELETE /v1/files/:id`). All share links of the file stop working immediately and
the key's used bytes drop. Prints `deleted <id>` per file; with `--json`, `{ "deleted": [ids] }`.
Exit 1 if any ID was not found (others are still deleted).

### `aispace revoke`

```
aispace revoke <link_id> [<link_id>...] [--json]
```

Revokes share links (`DELETE /v1/links/:id`). The file remains. Idempotent. Link IDs come from
`upload --link --json`, `link --json`, or `aispace links <file_id>`.

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

```
key      research-bot  used 1.0 MB / 5.0 MB   (4.0 MB remaining)
account  free          used 3.0 MB / 10.0 MB  (7.0 MB remaining), 0 blocks
month    uploads 12/100, downloads 340/1000, resets 2026-10-01 00:00 UTC
limits   max file 5.0 MB, file ttl <= 7d, link ttl <= 7d, keys <= 2   (Paid: 100 MB, 30d, 30d, 20)
rate     uploads 58/60 this hour, 490/500 today, 299/300 requests this minute
```

The `month` row is the account-wide monthly cap; hitting it hard-stops uploads
(`429 monthly_upload_cap`) or downloads (`429 monthly_download_cap`) until the reset shown.

```sh
aispace quota --json | jq '.key.remaining_bytes'
aispace quota --json | jq -e '.rate.uploads_hour_remaining > 0' >/dev/null || echo "wait"

# uploads left this month, and when the counter resets
aispace quota --json | jq -r '"\(.month.uploads_limit - .month.uploads_used) uploads left, resets \(.month.period_end | todate)"'
```

### `aispace whoami`

Prints key name, prefix and account email (`GET /v1/whoami`). Exit 3 if the key is invalid.

### `aispace version`

```
aispace 0.3.1 (commit abc1234, go1.26.6, darwin/arm64)
```

`--json` → `{ "version": "0.3.1", "commit": "abc1234", "go": "go1.26.6", "os": "darwin", "arch": "arm64" }`.

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
