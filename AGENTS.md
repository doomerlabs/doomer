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
