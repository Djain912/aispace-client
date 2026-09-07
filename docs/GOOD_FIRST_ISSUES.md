# Good first issue drafts

Publish these after the visibility changes land. Apply the `good first issue` label plus the label
shown for each draft. Remove a draft from this file after creating its GitHub issue.

## Add a GitLab CI report-sharing example

**Labels:** `good first issue`, `documentation`

Add `examples/gitlab-ci-report.yml`, equivalent in scope and safety to
`examples/github-actions-report.yml`. It should run only as a manually triggered job, read the key
from a masked CI variable, pin the aispace version, create a one-hour link with at most three
downloads, and expose the result without printing the key.

Acceptance criteria:

- The YAML is valid and has a manual trigger.
- The example documents the required `AISPACE_KEY` variable.
- The README in `examples/` links to it.
- No credential, real file ID, or live private link appears in the fixture.

## Add unsupported-platform npm installer coverage

**Labels:** `good first issue`, `testing`

Extend `npm/test/install.test.js` to prove that the npm installer fails with a concise actionable
message on an unsupported operating-system/architecture pair. Reuse the existing test harness and
do not make a network request.

Acceptance criteria:

- The new test is deterministic and offline.
- The error names the unsupported OS and architecture.
- `cd npm && npm test` passes.

## Add a JSON contract example for every exit-code class

**Labels:** `good first issue`, `documentation`, `testing`

Add sanitized JSON error examples for usage, authentication, quota, and rate-limit failures to
`docs/CLI.md`, and add or point to a test that locks the documented `exit_code` values.

Acceptance criteria:

- Examples match the current `{"error":{...}}` envelope.
- No production endpoint, credential, private link, or file ID is used.
- Documentation and tests agree on exit codes 2–5.
