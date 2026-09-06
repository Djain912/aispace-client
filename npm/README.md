# aispace CLI for npm

Installs the native [`aispace`](https://github.com/aispace-sh/aispace-client) binary for macOS or
Linux and verifies it against the SHA-256 checksums published with the matching GitHub release.

```sh
npm install -g @aispace-sh/cli
aispace login --key ask_...
```

Supported platforms: macOS and Linux on x64 or ARM64. Node.js 20 or newer is required for the
installer and wrapper; the installed Go binary itself has no Node.js runtime dependency.
