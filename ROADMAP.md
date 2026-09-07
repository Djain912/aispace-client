# Roadmap

aispace aims to be the smallest dependable bridge for temporary files between agents, humans, and
automation. The roadmap is intentionally outcome-oriented; opening an issue before implementing a
large item helps confirm the intended shape.

## Now: dependable first release

- Publish checksum-verified binaries for macOS, Linux, and Windows on amd64 and arm64.
- Publish matching npm and Homebrew packages from the same immutable GitHub release.
- Keep CLI output, JSON shapes, errors, and exit codes stable and documented.
- Validate the Codex skill and agent integration examples against the published client.

## Next: easier integrations

- Add copyable examples for more CI systems and agent runtimes.
- Evaluate Windows code signing and a native package-manager channel.
- Add opt-in shell completion installation.
- Improve diagnostics for network, quota, and configuration failures.

## Later: larger and longer-running workflows

- Evaluate resumable uploads and downloads without weakening integrity checks.
- Explore batch operations with explicit concurrency and quota controls.
- Publish compatibility fixtures for third-party client implementations.

## Non-goals

- Permanent file storage or synchronization.
- Hiding credentials inside prompts or public links.
- Bundling the hosted service, billing system, deployment configuration, or customer data in this
  repository.

Suggest or discuss roadmap items through GitHub Issues. Security reports belong in private GitHub
Security Advisories.
