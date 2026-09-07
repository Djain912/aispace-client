# Launch kit

Use this kit after the first release is installed successfully from GitHub, npm, and Homebrew. Replace
every bracketed placeholder and verify links before publishing.

## Canonical description

aispace is secure temporary file sharing for AI agents and humans: a scriptable CLI with expiring
links, predictable JSON, download caps, revocation, and optional local age encryption.

## Short listing

**Name:** aispace

**Tagline:** Secure temporary file sharing for AI agents and humans.

**Description:** Upload generated reports, archives, images, and other artifacts from a shell or AI
agent. Return short-lived links with optional download caps, or hand files to another authenticated
key without creating a public URL. JSON output and stable exit codes make automation predictable;
optional age X25519 encryption keeps plaintext off the service.

**Repository:** https://github.com/aispace-sh/aispace-client

**Website:** https://aispace.sh

**Suggested categories:** AI agents, developer tools, CLI, file sharing, security, automation.

## Show HN draft

**Title:** Show HN: aispace – Secure temporary file sharing for AI agents

I built aispace because agents are good at producing files but handing those files to a person is
still awkward. Base64 in chat is noisy, permanent cloud storage is excessive, and generic upload
tools rarely provide predictable machine output or safe defaults.

aispace is a small open-source Go CLI and agent skill. It streams uploads, emits stable JSON and exit
codes, creates separately expiring links with optional download caps, and can encrypt locally with
age X25519 before upload. The generated identity never reaches the service.

The client is open source; the hosted storage service is operated separately. The repository is
explicit about that boundary and contains no server, billing, deployment, or customer data.

The workflow is:

```sh
aispace upload report.pdf --link --link-expires 1h --max-downloads 1 --json
```

Repository: https://github.com/aispace-sh/aispace-client

I would especially value feedback on the CLI contract, threat-model documentation, and which agent
runtimes deserve first-class examples next.

## Technical article outline

**Working title:** Designing a file-sharing CLI that AI agents can use safely

1. Why chat attachments and permanent object storage are poor defaults for generated artifacts.
2. Designing stdout, stderr, JSON shapes, and exit codes as an automation contract.
3. Separating authenticated file visibility from public-link capabilities.
4. Choosing file and link expiration independently.
5. Encrypting locally with age and treating identities as credentials.
6. Handling retries, quotas, checksums, and partial failures.
7. A complete agent tool definition and transcript.
8. What remains proprietary and what the open-source client can verify.

End with the smallest working command and the repository link; avoid a generic product announcement.

## Social posts

### Release post

aispace v[VERSION] is available: secure temporary file sharing built for AI agents and shell
automation.

Stable JSON. Expiring and revocable links. Download caps. Optional local age encryption.

[RELEASE URL]

### Demonstration post

An agent generated a report. One command turned it into a one-hour link with a one-download cap:

`aispace upload report.pdf --link --link-expires 1h --max-downloads 1`

The URL is always the last output line; `--json` makes the entire exchange machine-readable.

[DEMO URL]

## Submission checklist

- GitHub release, npm, Homebrew, and shell installer all return the same version.
- Applicable README install commands work on clean macOS, Linux, and Windows environments.
- Social preview is uploaded in GitHub repository settings.
- Repository description, website, and topics match the canonical wording above.
- The demo contains no real key, private link, file ID, or encryption identity.
- Submit only to directories that accept hosted developer tools or agent skills; do not describe the
  client as an MCP server or the hosted backend as open source.
- Record the publication URL, date, copy, and referral/source tag for each post.

Suggested first venues: Show HN, relevant developer-tool and AI-agent communities, the Go community,
and agent-skill directories whose current format accepts a repository-contained `SKILL.md`.
