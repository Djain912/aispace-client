# aispace — secure temporary file sharing for AI agents

Installs the native [`aispace`](https://github.com/aispace-sh/aispace-client) binary for macOS,
Linux, or Windows and verifies it against the SHA-256 checksums published with the matching GitHub
release. The CLI creates expiring and revocable file links, emits predictable JSON for automation,
and can encrypt files locally with age X25519 before upload.

```sh
npm install -g @aispace-sh/cli
aispace login --key ask_...
aispace upload report.pdf --link --link-expires 1h --json
```

Supported platforms: macOS, Linux, and Windows on x64 or ARM64. Node.js 20 or newer is required for
the installer and wrapper; the installed Go binary itself has no Node.js runtime dependency.
Windows binaries are checksum-verified but not currently code-signed, so Windows may show a
SmartScreen warning on first run.

See the [main repository](https://github.com/aispace-sh/aispace-client) for the agent skill,
encrypted-handoff examples, CLI/API documentation, threat boundaries, and contribution guide.
