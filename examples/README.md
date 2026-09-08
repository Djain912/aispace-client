# aispace examples

These examples show common agent and automation workflows. They make real API calls, so install
`aispace`, authenticate with `aispace login` or `AISPACE_KEY`, and inspect `aispace quota --json`
before uploading a large artifact or running a batch.

These scripts demonstrate the stable legacy file API and age encryption. For experimental sealed
bundles, identity-addressed inboxes, device pairing, adaptive R2 fallback, and recovery, follow
[`../docs/SECURE_HANDOFFS.md`](../docs/SECURE_HANDOFFS.md).

## Share a generated report

[`share-report.sh`](share-report.sh) uploads one file and emits a small JSON handoff containing the
URL, expiration, file ID, and link ID:

```sh
./examples/share-report.sh ./report.pdf
AISPACE_LINK_EXPIRES=8h AISPACE_MAX_DOWNLOADS=3 ./examples/share-report.sh ./report.pdf
```

Public links require a Pro account. The script fails before uploading when the account cannot mint
links.

## Create an encrypted handoff

[`encrypted-handoff.sh`](encrypted-handoff.sh) encrypts a file locally with age X25519, uploads only
the ciphertext, and writes the generated secret identity beside the source file:

```sh
./examples/encrypted-handoff.sh ./customer-export.csv
```

Send the URL and identity file through separate authenticated channels. Anyone holding both can
decrypt the artifact; losing the identity makes it unrecoverable.

## Publish a CI report

[`github-actions-report.yml`](github-actions-report.yml) is a copyable GitHub Actions job. It uploads
a test report only on an explicit `workflow_dispatch`, then writes the expiring URL to the workflow
summary. Public links require Pro. Add `AISPACE_KEY` as a repository secret before using it.

## Wire aispace into an LLM

[`../docs/LLM_USAGE.md`](../docs/LLM_USAGE.md) contains:

- a system-prompt snippet;
- OpenAI- and Anthropic-compatible JSON tool definitions;
- a Python `subprocess` wrapper around the CLI; and
- a complete example conversation.

For Codex, use the ready-made [`../skills/aispace/SKILL.md`](../skills/aispace/SKILL.md).
