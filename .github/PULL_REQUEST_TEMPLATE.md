## What changed

<!-- Describe the user-visible behavior and why it belongs in this pull request. -->

## Verification

- [ ] `go test -race ./...`
- [ ] `go vet ./...`
- [ ] `test -z "$(gofmt -l .)"`
- [ ] `(cd npm && npm test && npm pack --dry-run)`
- [ ] Public CLI/API/JSON behavior is documented, or documentation is not affected.
- [ ] No API key, private link, file ID, or encryption identity appears in the diff or test output.

## Compatibility

<!-- Note changes to commands, flags, JSON, errors, exit codes, installation, or supported platforms. -->
