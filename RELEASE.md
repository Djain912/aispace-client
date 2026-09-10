# Release process

The manual `Release` workflow publishes one version through GitHub Releases, npm, Homebrew, and the
official MCP Registry. GitHub Releases is the source of truth: the npm installer and Homebrew cask
both download its checksum-verified native binaries. MCP publication runs last, after the installed
npm package has completed a real stdio handshake on Linux, macOS, and Windows.

## One-time configuration

1. Create the public `aispace-sh/homebrew-tap` repository with a `Casks` directory.
2. Add a fine-grained GitHub token as the `HOMEBREW_TAP_GITHUB_TOKEN` repository secret. Restrict it
   to `aispace-sh/homebrew-tap` with Contents read/write permission.
3. Publish `@aispace-sh/cli` once from the `aispace-sh` npm organization, using a granular automation
   token stored as the `NPM_TOKEN` repository secret.
4. The current workflow verifies and uses `NPM_TOKEN` and also grants `id-token: write` for npm
   provenance. Do not remove the token until the workflow is deliberately migrated and tested with
   npm trusted publishing.
5. Generate a dedicated Ed25519 key for MCP Registry ownership, publish its public key in the
   `aispace.sh` DNS TXT record required by the Registry, and store only the private-key hex as the
   `MCP_PRIVATE_KEY` repository secret. Never commit the private key. The workflow authenticates the
   stable `sh.aispace/mcp` namespace with this key.
6. Protect `main` and `v*` tags, require CI, and restrict release-secret access to maintainers.

## Release

1. Open **Actions → Release → Run workflow**.
2. Select the `main` branch and enter a version without the `v` prefix, such as `0.1.0`.
3. Start the workflow. It validates the version and credentials, runs the Go and npm test suites,
   stamps npm and `server.json` from the workflow version, performs GoReleaser, npm, and MCP metadata
   validation, and only then creates the tag. GoReleaser uploads into a draft; the workflow publishes
   it only after all required assets and the Homebrew cask have been verified.
4. Confirm the package verification jobs install the published npm package, report the requested
   version, and initialize `aispace mcp serve` on Linux, macOS, and Windows.
5. Confirm the final job publishes `sh.aispace/mcp` and finds the exact version and npm package in
   the Registry API.

### Resume a partial release

If a run fails after creating its tag, rerun the workflow from the same `main` commit with the same
version and select **Resume**. Resume mode refuses a tag that points anywhere else. It removes only
an incomplete draft GitHub release, rebuilds that draft, and preserves already-published GitHub,
npm, and Registry versions. The workflow verifies the native npm assets, installer, and Homebrew
cask before publishing the draft and testing installation again on all three operating systems.

If only MCP publication failed, run the workflow from any branch, enter the existing release
version, and select **MCP only**. This mode resolves the immutable release tag rather than current
`main`, verifies that the matching npm version exists, and then publishes or verifies only the MCP
Registry record. It is safe to use after `main` has advanced. If the exact Registry version already
exists, the workflow preserves it and only verifies its metadata.

Do not select Resume to rebuild or replace published artifacts. If the published GitHub assets or
Homebrew cask fail verification, investigate the channel and publish a new patch version.

The release job must finish before npm publication starts, because the npm postinstall script
downloads the native binary and `checksums.txt` from that GitHub release. GoReleaser updates
`Casks/aispace.rb` in the tap during the same release job. Registry publication must finish after npm
because the Registry validates the public package and its `mcpName` ownership marker.

## Verify

On macOS or Linux:

```sh
npm install -g @aispace-sh/cli
aispace version

brew install aispace-sh/tap/aispace
aispace version
```

On Windows, verify the npm package from PowerShell:

```powershell
npm install -g @aispace-sh/cli
aispace version
```

For the MCP command, use a client or the workflow smoke harness rather than entering protocol data
manually. Initialization and tool listing do not contact the aispace API; API calls require a real
scoped key.

Never reuse or move a published tag. Resume may finish missing channels for the exact tagged commit,
but it never replaces an immutable published release or npm version.
