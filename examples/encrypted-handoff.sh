#!/usr/bin/env sh
set -eu

if [ "$#" -ne 1 ]; then
  echo "usage: $0 PATH" >&2
  exit 2
fi

command -v aispace >/dev/null 2>&1 || {
  echo "error: aispace is not installed" >&2
  exit 3
}
command -v jq >/dev/null 2>&1 || {
  echo "error: jq is required for this example" >&2
  exit 3
}

path=$1
identity_path="${path}.agekey"

if [ -e "$identity_path" ]; then
  echo "error: refusing to overwrite $identity_path" >&2
  exit 2
fi

quota=$(aispace quota --json)
plan=$(printf '%s' "$quota" | jq -r '.account.plan')
if [ "$plan" != "pro" ]; then
  echo "error: public links require an aispace Pro account" >&2
  exit 4
fi

result=$(aispace upload "$path" \
  --encrypt \
  --identity-out "$identity_path" \
  --link \
  --link-expires 1h \
  --max-downloads 1 \
  --json)

printf '%s\n' "$result" | jq --arg identity_path "$identity_path" '{
  url: .link.url,
  expires_at: .link.expires_at,
  file_id: .file.id,
  link_id: .link.id,
  recipient: .encryption.recipient,
  identity_path: $identity_path
}'

echo "Keep $identity_path private and send it separately from the URL." >&2
