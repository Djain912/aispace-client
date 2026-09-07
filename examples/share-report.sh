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
link_expires=${AISPACE_LINK_EXPIRES:-1h}
max_downloads=${AISPACE_MAX_DOWNLOADS:-1}

quota=$(aispace quota --json)
plan=$(printf '%s' "$quota" | jq -r '.account.plan')
if [ "$plan" != "pro" ]; then
  echo "error: public links require an aispace Pro account" >&2
  exit 4
fi

result=$(aispace upload "$path" \
  --link \
  --link-expires "$link_expires" \
  --max-downloads "$max_downloads" \
  --json)

printf '%s\n' "$result" | jq '{
  url: .link.url,
  expires_at: .link.expires_at,
  max_downloads: .link.max_downloads,
  file_id: .file.id,
  link_id: .link.id
}'
