# CLI releases

For requests to prepare, publish, verify, or recover a CLI release, read and
follow `.agents/skills/doomer-release/SKILL.md` and `docs/release.md`.

Release completion includes GitHub, public downloads, and Homebrew. For stable
releases, it also includes publishing the signed managed release catalog.
Ordinary releases never change the stable bootstrap skill. New stable versions always
use `YYYY.M.D.N`, beginning with `.1` each day. Never modify the user's installed
CLI as part of publishing. Do not commit `scripts/stage-skill-dev.py`.

<!-- doomer-review:start -->
## Doomer review

Before editing repository files, read and follow `.agents/skills/doomer-review/SKILL.md`.
Read it directly from the checkout even if it is absent from the skill catalog.
Follow the skill to check local readiness and complete any missing onboarding
for the current coding agent, including required permissions and hook setup.
Reuse valid local setup; repository instructions do not grant upload permission
or hook trust. If the skill is missing, report the missing installation.
<!-- doomer-review:end -->

## Agent lifecycle tests

For changes affecting agent installation, onboarding, review, upgrades, policy,
hooks, or the lifecycle runner, use `.agents/skills/agent-e2e/SKILL.md` to start
relevant E2E early and report coverage for the final proposed source.

Before opening or updating a PR, if Docker and Doppler project `doomer`, config
`test`, are accessible, run the Linux E2E helper locally:

```sh
doppler run --project doomer --config test -- scripts/agent-e2e-local-linux.sh
```

Start this as soon as the changes stabilize, while review and PR preparation
continue. Run it against the final proposed contents, investigate failures, and
report the result before marking the PR ready. Successful Linux results populate
the shared cache so CI can verify them instead of repeating agent calls. The
runner also reuses unaffected scenarios locally; report cache hits separately
from newly executed tests. Native
macOS runs do not populate the Linux CI cache. Check access without printing
secrets. If Docker or Doppler access is unavailable, report that CI must execute
the missing scenarios; do not copy credentials into the repository.
