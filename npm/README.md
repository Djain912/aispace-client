# aispace — secure temporary file sharing for AI agents

Installs the native [`aispace`](https://github.com/aispace-sh/aispace-client) binary for macOS,
Linux, or Windows and verifies it against the SHA-256 checksums published with the matching GitHub
release. The CLI creates expiring and revocable file links, emits predictable JSON for automation,
and can encrypt files locally with age X25519 before upload.

Installer downloads are HTTPS-only and restricted to GitHub release hosts. The binary is streamed
to a bounded same-directory temporary file, verified, and atomically renamed into place; stalled
downloads and oversized responses fail without leaving a partial executable.

```sh
npm install -g @aispace-sh/cli
aispace login --key ask_...
aispace upload report.pdf --link --link-expires 1h --json
```

## MCP server

The package also installs the native local MCP server used by Codex, Claude Code, and other stdio
hosts. Create a separately scoped bot key in the aispace dashboard, inject it through the host
environment, and launch:

```sh
AISPACE_KEY=ask_... aispace mcp serve
```

Configure the host with command `aispace`, arguments `mcp`, `serve`, and secret environment
variable `AISPACE_KEY`. Optional `AISPACE_ALLOWED_ROOTS` limits local upload and download paths
using the platform path-list separator. Uploads are private by default; public links, deletion,
revocation, and download overwrite remain explicit tool calls.

The server is published as `sh.aispace/mcp` in the official MCP Registry. See the main repository's
[MCP documentation](https://github.com/aispace-sh/aispace-client#model-context-protocol-mcp) for
complete Codex, Claude Code, and generic-host configuration.

Supported platforms: macOS, Linux, and Windows on x64 or ARM64. Node.js 20 or newer is required for
the installer and wrapper; the installed Go binary itself has no Node.js runtime dependency.
Windows binaries are checksum-verified but not currently code-signed, so Windows may show a
SmartScreen warning on first run.

See the [main repository](https://github.com/aispace-sh/aispace-client) for the agent skill,
encrypted-handoff examples, CLI/API documentation, threat boundaries, and contribution guide.
