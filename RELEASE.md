# Release process

The manual `Release` workflow publishes one version through GitHub Releases, npm, and Homebrew.
GitHub Releases is the source of truth: the npm installer and Homebrew cask both download its
checksum-verified native binaries.

## One-time configuration

1. Create the public `aispace-sh/homebrew-tap` repository with a `Casks` directory.
2. Add a fine-grained GitHub token as the `HOMEBREW_TAP_GITHUB_TOKEN` repository secret. Restrict it
   to `aispace-sh/homebrew-tap` with Contents read/write permission.
3. Publish `@aispace-sh/cli` once from the `aispace-sh` npm organization, using a granular automation
   token stored as the `NPM_TOKEN` repository secret.
4. After the first npm release, configure npm trusted publishing for repository
   `aispace-sh/aispace-client` and workflow `release.yml`, then remove `NPM_TOKEN` and its workflow
   environment entry. Keep `id-token: write`; npm will publish with short-lived OIDC credentials and
   automatic provenance.
5. Protect `main` and `v*` tags, require CI, and restrict release-secret access to maintainers.

## Release

1. Open **Actions → Release → Run workflow**.
2. Select the `main` branch and enter a version without the `v` prefix, such as `0.1.0`.
3. Start the workflow. It validates the version and credentials, runs the Go and npm test suites,
   performs GoReleaser and npm publication dry runs, and only then creates the tag and publishes.
4. Confirm the final verification job installs the published npm package and reports the requested
   version.

The release job must finish before npm publication starts, because the npm postinstall script
downloads the native binary and `checksums.txt` from that GitHub release. GoReleaser updates
`Casks/aispace.rb` in the tap during the same release job.

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

Never reuse or move a published tag. Publish a new patch version if any channel fails after an
immutable npm version has been created.
