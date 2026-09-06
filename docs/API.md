# aispace client API

The CLI uses the public bot API at `https://aispace.sh`. Compatible endpoints can be selected with
`AISPACE_URL` or `--url`.

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
| `DELETE` | `/v1/files/:id` | Delete a file and invalidate its links |
| `POST` | `/v1/files/:id/links` | Create an expiring share link |
| `GET` | `/v1/files/:id/links` | List link metadata; tokens are not returned |
| `DELETE` | `/v1/links/:id` | Revoke a share link |
| `GET`, `HEAD` | `/d/:token` | Download through a public share link |

## Upload

Uploads are raw bodies rather than multipart forms:

```sh
curl -fsS -X POST https://aispace.sh/v1/files \
  -H "Authorization: Bearer $AISPACE_KEY" \
  -H "X-File-Name: report.pdf" \
  -H "Content-Type: application/pdf" \
  --data-binary @report.pdf
```

Optional headers are `X-Expires-In` and `X-SHA256`. The server requires `Content-Length` and
returns the stored file metadata as JSON.

## Create a link

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

For exact request and response types, see [`internal/api/types.go`](../internal/api/types.go) and
[`internal/api/client.go`](../internal/api/client.go), which are the executable client contract.
