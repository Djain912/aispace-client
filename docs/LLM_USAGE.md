# Giving aispace to an LLM agent

aispace lets an agent store temporary files, exchange them with sibling keys on the same account,
and explicitly hand a human a file through an expiring public link. This document shows how to wire it up in the three common shapes: a shell tool, a JSON tool
schema, and a system-prompt snippet.

Prerequisites: a human created a bot key in the dashboard and gave it to the agent's runtime as
`AISPACE_KEY` (preferred: an environment variable, never pasted into the prompt), and the
`aispace` CLI is on `PATH` (`curl -fsSL https://aispace.sh/install.sh | sh`).

Public links require Pro. On Free, or between keys belonging to the same account, use an explicit
`--shared` upload and exchange the file ID. For sealed bundles, addressed inboxes, identity trust,
device pairing, and their feature-availability checks, use [Secure handoffs](SECURE_HANDOFFS.md).

## System prompt snippet

```
You can share files with the user through the `aispace` CLI. Use it whenever an output is too
long or too structured for chat (reports, tables, code bundles, images).

- Upload and link in one call:  echo "$CONTENT" | aispace upload - --name NAME --link --link-expires 1h --json
  or for a file on disk:        aispace upload PATH --link --link-expires 1h --json
- Parse `.link.url` from the JSON and give that URL to the user, together with when it expires.
- Do not add `--link` for ordinary storage or same-account agent handoffs. Add `--shared`
  explicitly; account sharing is disabled by default. Sibling agents then use
  `aispace download FILE_ID --output PATH`.
- Default to a 1-hour link (`--link-expires 1h`). Use up to 24h only if the user says they will
  read it later. Add `--max-downloads 1` for anything sensitive.
- Files themselves expire after 7 days by default (`--expires`); the maximum is 7 days on the
  free plan and 30 days on Pro. Never promise permanence.
- Before uploading anything large, or before a batch of uploads, run `aispace quota --json` and
  check `.limits.max_file_bytes`, `.key.remaining_bytes`, `.account.remaining_bytes` and the
  short-window counters `.rate.uploads_hour_remaining` / `.rate.uploads_day_remaining`, plus
  `.month.uploads_used < .month.uploads_limit`.
- Check `.month.uploads_used` / `.month.uploads_limit` and the corresponding download fields before
  large workflows. `.month.period_end` is the next UTC reset.
- Exit codes: 2 = bad input, fix the arguments and do not retry unchanged; 3 = key problem (stop
  and tell the user); 4 = storage quota exceeded (tell the user, do not retry); 5 = rate limited
  or the monthly cap is used up.
- If the error code is `monthly_upload_cap` or `monthly_download_cap` the account is out of
  requests for the whole calendar month: do NOT retry or wait, tell the user the cap is reached
  and that they can upgrade in the dashboard or mail contacts@aispace.sh.
- Never print the AISPACE_KEY. Never upload secrets or credentials.
- If the user requests client-side encryption, add `--encrypt`. Prefer `--identity-out PATH`
  so the secret identity does not enter the transcript. Return the URL and explain that the
  identity must be delivered separately; never upload the identity to aispace.
- A receiving agent decrypts locally with `aispace decrypt URL --identity-file PATH --output PATH`.
  Treat an `AGE-SECRET-KEY-...` identity like a password. Encryption authenticates ciphertext
  integrity, not the sender's identity.
```

## Shell tool loop (bash)

A minimal harness where the model emits shell commands and the harness runs them. The relevant
part is how the upload result is fed back.

```sh
#!/usr/bin/env bash
# share.sh — produce an artifact, upload it, return a link. Used as a tool by the agent.
set -euo pipefail
name="$1"; ttl="${2:-1h}"; maxdl="${3:-}"

args=(upload - --name "$name" --link --link-expires "$ttl" --json)
[ -n "$maxdl" ] && args+=(--max-downloads "$maxdl")

# quota pre-check (cheap: one GET)
if ! aispace quota --json | jq -e '.rate.uploads_hour_remaining > 0 and .key.remaining_bytes > 0' >/dev/null; then
  echo '{"error":"quota or rate limit exhausted; ask the user to raise the key budget"}'; exit 4
fi

if out=$(aispace "${args[@]}" 2>err.txt); then
  jq -c '{url: .link.url, expires_at: .link.expires_at, file_id: .file.id, link_id: .link.id, size: .file.size_bytes}' <<<"$out"
else
  code=$?
  jq -nc --arg e "$(cat err.txt)" --argjson c "$code" '{error: $e, exit: $c}'
  exit "$code"
fi
```

Usage from the model's side: `python gen_report.py | ./share.sh report.md 2h 3`.

## Tool JSON schema

Three tools that wrap the CLI. The shapes work unchanged as OpenAI `tools[].function` entries or as
Anthropic `tools[]` entries (rename `parameters` → `input_schema` for Anthropic).

```json
[
  {
    "name": "aispace_upload",
    "description": "Upload a file or text to aispace and optionally create an expiring share link for the user. Returns the file id and, if requested, the link URL and expiry. Files expire automatically (default 7 days; maximum 7 days on the free plan, 30 on Pro). Each call consumes one of the account's monthly uploads. Use for outputs too large or structured for chat.",
    "parameters": {
      "type": "object",
      "properties": {
        "name": { "type": "string", "description": "Filename with extension, e.g. report.md, data.csv. Max 200 chars." },
        "content": { "type": "string", "description": "UTF-8 text content to upload. Provide either content or path." },
        "path": { "type": "string", "description": "Path to an existing local file to upload. Provide either content or path." },
        "content_type": { "type": "string", "description": "MIME type. Defaults from the extension." },
        "expires": { "type": "string", "description": "File lifetime such as 1h, 3d, 7d. Maximum 7d on the free plan, 30d on Pro; longer values are rejected. Default 7d.", "default": "7d" },
        "link": { "type": "boolean", "description": "Also create a Pro-only public share link. Enable only when sharing outside the account is requested.", "default": false },
        "link_expires": { "type": "string", "description": "Pro public-link lifetime such as 15m, 1h, 24h. Maximum 30d and never past the file's own expiry; longer values are clamped. Default 1h.", "default": "1h" },
        "max_downloads": { "type": "integer", "minimum": 1, "description": "Optional cap on downloads for the link. Use 1 for sensitive one-shot delivery." },
        "encrypt": { "type": "boolean", "description": "Encrypt locally with age X25519 before upload. The service stores ciphertext only.", "default": false },
        "recipient": { "type": "string", "description": "Optional age1... public recipient owned by the receiver. If omitted while encrypt=true, a one-time secret identity is generated." },
        "identity_out": { "type": "string", "description": "Optional local path for the generated identity; created mode 0600 and never overwritten. Do not combine with recipient." }
      },
      "required": ["name"]
    }
  },
  {
    "name": "aispace_link",
    "description": "On Pro, create a new expiring share link for a file that was already uploaded to aispace (by file_id). Use when the previous link expired or a second recipient needs a separate link.",
    "parameters": {
      "type": "object",
      "properties": {
        "file_id": { "type": "string", "description": "The file id returned by aispace_upload." },
        "expires": { "type": "string", "description": "Pro public-link lifetime such as 15m, 1h, 24h. Maximum 30d and never past the file's own expiry. Default 1h.", "default": "1h" },
        "max_downloads": { "type": "integer", "minimum": 1 }
      },
      "required": ["file_id"]
    }
  },
  {
    "name": "aispace_decrypt",
    "description": "Download if necessary and decrypt an age-encrypted aispace artifact locally. The decryption identity remains on the client.",
    "parameters": {
      "type": "object",
      "properties": {
        "source": { "type": "string", "description": "A local .age path or an https://aispace.sh/d/... share URL." },
        "identity_file": { "type": "string", "description": "Local mode-0600 file containing the AGE-SECRET-KEY identity." },
        "output": { "type": "string", "description": "New local plaintext output path. Existing files are never overwritten." }
      },
      "required": ["source", "identity_file", "output"]
    }
  }
]
```

Reference implementation of the three tool handlers (Python, subprocess over the CLI):

```python
import json, subprocess, tempfile, os

def _run(args, stdin=None):
    p = subprocess.run(["aispace", *args, "--json"], input=stdin, capture_output=True, text=True)
    if p.returncode != 0:
        kind = {3: "auth", 4: "quota", 5: "rate_limited", 2: "usage"}.get(p.returncode, "error")
        return {"error": kind, "detail": p.stderr.strip()}
    return json.loads(p.stdout)

def aispace_upload(name, content=None, path=None, content_type=None, expires="7d",
                   link=False, link_expires="1h", max_downloads=None,
                   encrypt=False, recipient=None, identity_out=None):
    args = ["upload", path or "-", "--name", name, "--expires", expires]
    if content_type: args += ["--content-type", content_type]
    if encrypt:
        args += ["--encrypt"]
        if recipient: args += ["--recipient", recipient]
        if identity_out: args += ["--identity-out", identity_out]
    if link:
        args += ["--link", "--link-expires", link_expires]
        if max_downloads: args += ["--max-downloads", str(max_downloads)]
    out = _run(args, stdin=content if path is None else None)
    if "error" in out: return out
    if encrypt:
        result = {"file_id": out["file"]["id"], "size_bytes": out["file"]["size_bytes"],
                  "expires_at": out["file"]["expires_at"], "encryption": out["encryption"]}
        if link:
            result.update({"url": out["link"]["url"], "link_id": out["link"]["id"],
                           "expires_at": out["link"]["expires_at"]})
        return result
    if link:
        return {"file_id": out["file"]["id"], "size_bytes": out["file"]["size_bytes"],
                "url": out["link"]["url"], "link_id": out["link"]["id"],
                "expires_at": out["link"]["expires_at"]}
    return {"file_id": out["id"], "size_bytes": out["size_bytes"], "expires_at": out["expires_at"]}

def aispace_link(file_id, expires="1h", max_downloads=None):
    args = ["link", file_id, "--expires", expires]
    if max_downloads: args += ["--max-downloads", str(max_downloads)]
    out = _run(args)
    if "error" in out: return out
    return {"url": out["url"], "link_id": out["id"], "expires_at": out["expires_at"]}

def aispace_decrypt(source, identity_file, output):
    return _run(["decrypt", source, "--identity-file", identity_file, "--output", output])
```

If you prefer not to ship the CLI in the agent's sandbox, upload and link creation are HTTP calls:
`POST /v1/files` (raw body + `X-File-Name`) then `POST /v1/files/:id/links`. See
[API.md](API.md).

## Example transcript

This public-link example requires Pro.

```
user:  Compare the three vendor quotes in ./quotes/*.pdf and give me something I can forward to
       finance.

agent: (reads the PDFs, builds a comparison)
       [tool aispace_upload {"name": "vendor-comparison.md", "content": "# Vendor comparison\n...",
                             "link": true, "link_expires": "1h", "max_downloads": 3}]
tool:  {"file_id": "01J8ZQ3V9N7X2K4M6P8R0T2W4Y", "size_bytes": 4812,
        "url": "https://aispace.sh/d/K3f9Qm2xP7vL1nR8sT4wY6zA0bC5dE9fG3hJ7kM2nP4",
        "link_id": "01J8ZQ4A2B4C6D8E0F2G4H6J8K", "expires_at": 1757003600}

agent: Done. Vendor B is cheapest over 3 years (€41.2k vs €44.9k and €52.0k) once the mandatory
       support tier is included; details, assumptions and a per-line table are in the report:

       https://aispace.sh/d/K3f9Qm2xP7vL1nR8sT4wY6zA0bC5dE9fG3hJ7kM2nP4

       The link works for 1 hour and at most 3 downloads. Tell me if finance needs a longer
       window and I will mint a fresh link.

user:  They'll look tomorrow morning.

agent: [tool aispace_link {"file_id": "01J8ZQ3V9N7X2K4M6P8R0T2W4Y", "expires": "24h"}]
tool:  {"url": "https://aispace.sh/d/P8vL1nR8sT4wY6zA0bC5dE9fG3hJ7kM2nP4qR6sT8uV0",
        "link_id": "01J8ZQ7Q...", "expires_at": 1757086400}

agent: New link, valid for 24 hours, no download cap:
       https://aispace.sh/d/P8vL1nR8sT4wY6zA0bC5dE9fG3hJ7kM2nP4qR6sT8uV0
```

Note the agent did not re-upload; it minted a second link on the existing file. The first link
still works until it expires, is exhausted, or the agent runs `aispace revoke`.

## Choosing expirations and download caps

| Situation | `--expires` (file) | `--link-expires` | `--max-downloads` |
|---|---|---|---|
| Result the user will open right now | 1d | 15m–1h | 1–3 |
| Report for a meeting later today | 3d | 8h | none |
| Handoff to a third party by email | 7d (default) | 24h | 1 per recipient (mint one link each) |
| Log bundle for a support ticket | 7d | 7d (links are Pro-only; capped at 30d and never past the file's expiry) | none |
| Anything containing PII or credentials-adjacent data | 1d | 15m | 1 |
| Intermediate artefact between two agents | 1h | 15m | 1 |

Rules of thumb:

- The **link** should live just long enough for the human to click. The **file** can live longer
  so a second link can be minted without re-uploading. Prefer `link 1h, file 7d` to `link 7d`.
- `--max-downloads 1` turns a link into a one-time token; browsers and previewers may pre-fetch
  with `HEAD` (which does not count) but a second `GET` will 404. Use `2` or `3` if the user is
  likely to open it on two devices.
- Mint **one link per recipient**; revoking one does not affect the others and the counters show
  which issued link was used. aispace does not identify the person behind a public download.
- Never bump a file's expiry: it is fixed at upload. If you need it longer, re-upload.
- File bytes become unavailable after at most 7 days (Free) or 30 days (Pro). Say so when handing over
  anything the user might want to keep ("download it before Friday"). Read
  `limits.max_file_ttl_seconds` from `aispace quota --json` rather than assuming 30 days: on the
  free plan a `--expires 30d` is rejected; request no more than the reported maximum.
- **Every download counts** against the account's monthly download cap (1,000 free, 100,000 per
  Pro block), so do not hand the same link to a system that polls it. One deliberate download per
  recipient is the intent.

## Checking upload capacity before a big upload or batch

Uploads can be refused for **bytes** (`402`, storage), short-window rate limits (`429`), or the
account-wide monthly count (`429 monthly_upload_cap`). A `quota` GET can pre-check storage,
short-window limits, and the monthly cap:

```sh
need=2097152    # 2 MB
aispace quota --json | jq -e --argjson n "$need" '
  .limits.max_file_bytes >= $n
  and .key.remaining_bytes >= $n
  and .account.remaining_bytes >= $n
  and .month.uploads_used < .month.uploads_limit
  and .rate.uploads_hour_remaining > 0
  and .rate.uploads_day_remaining > 0' >/dev/null || { echo "cannot upload $need bytes now"; aispace quota; exit 4; }
```

Before a **batch** of N uploads, check both short-window upload counts and the total bytes in one go:

```sh
n=250; total=52428800   # 250 files, 50 MB together
aispace quota --json | jq -e --argjson n "$n" --argjson t "$total" '
  .key.remaining_bytes >= $t
  and .account.remaining_bytes >= $t
  and (.month.uploads_limit - .month.uploads_used) >= $n
  and .rate.uploads_hour_remaining >= $n
  and .rate.uploads_day_remaining >= $n' >/dev/null || {
    echo "batch of $n would exceed a cap; see aispace quota"; exit 4; }
```

Concurrent users can consume capacity after the pre-check, so a batch may still stop partway with
`429 monthly_upload_cap`. Treat that as terminal for the month rather than retrying, and report how
many of the N uploads completed.

If the batch does not fit, prefer **one archive over many files** (`tar czf - dir | aispace upload
- --name out.tgz`): it costs a single upload against the monthly cap instead of N. That is usually
the right fix on the free plan, where 100 uploads/month goes quickly.

Also budget the *downloads* a hand-off will cause: a link given to five people costs up to five
downloads (plus retries), against 1,000/month free.

What to tell the user for each failing term:

| Term | Message |
|---|---|
| `limits.max_file_bytes < need` | File is over the per-file cap (5 MB on Free, 100 MB on Pro). Split it, compress it, or ask the human to upgrade. |
| `key.remaining_bytes < need` | This key's budget is full. Delete old files (`aispace ls`, `aispace rm`) or ask the human to raise the budget in the dashboard. |
| `account.remaining_bytes < need` | The account is at its storage allowance (10 MB on Free, 10 GB per Pro block). The human can free space or add a block. |
| `.month.uploads_used >= .month.uploads_limit` or `429 monthly_upload_cap` | The account's monthly upload cap is used up. Nothing will upload until `.month.period_end`; tell the human to upgrade or mail contacts@aispace.sh. |
| `rate.uploads_hour_remaining == 0` | Rate limited; wait until the top of the hour or batch outputs into one archive. |

Every `/v1` response also carries `X-Quota-Remaining` (bytes left on the key), so a client that
tracks headers can skip the pre-check for small files.

## Safety notes for operators

- Give each agent its **own key** with the **smallest budget** that works (10–50 MB is plenty
  for text reports). A leaked key can only fill its own budget.
- Pass the key via environment, not via prompt. Agents echo prompts into logs.
- Instruct the agent not to upload secrets. aispace cannot tell a `.env` from a `.txt`.
- Downloads are `Content-Disposition: attachment`; HTML/SVG are neutralised. A link is safe to
  click but the *contents* are whatever the agent produced.
