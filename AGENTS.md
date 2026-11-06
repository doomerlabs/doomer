# CLI releases

For requests to prepare, publish, verify, or recover a CLI release, read and
follow `.agents/skills/doomer-release/SKILL.md` and `docs/release.md`.

Release completion includes GitHub, public downloads, and Homebrew. For stable
releases, it also includes updating the public installer skill in
`doomerlabs/skills`; beta releases never change that stable installer pin. New stable versions always
use `YYYY.M.D.N`, beginning with `.1` each day. Never modify the user's installed
CLI as part of publishing. Do not commit `scripts/stage-skill-dev.py`.

<!-- doomer-review:start -->
<!-- doomer-managed
package: doomerlabs/doomer
revision: 2c24e3e7c479737c0dd74b025d0d6a40b178f7ef
source: internal/watch/templates/agent-instructions.md
kind: block
-->
## Doomer code review

Use the installed doomer-review skill when editing code. Doomer's trusted hooks
capture tasks and edits and deliver review results; do not poll review state or
run setup on every edit. Investigate feedback privately, fix confirmed issues
within the user's scope, and record each response using the hook's instructions.
Include the complete Doomer review link once in the final answer, including when
no issues remain. Only report completion when the hook confirms that revision.
Repository upload consent and coding-agent hook trust are separate user choices.
If setup is missing, follow the skill's onboarding instructions. Never approve
uploads or hook trust on the user's behalf. Preserve a paused watcher.
<!-- doomer-managed:end -->
<!-- doomer-review:end -->

<!-- doomer-review-policy:start -->
<!-- doomer-managed
package: doomerlabs/skills
revision: 80297bbdbb57e8e71314213df3e2fceccc9bc1bf
source: templates/review-policy.md
kind: block
agents: codex,claude
-->
## Required Doomer review workflow

Codex uses .agents/skills/doomer-review/
Claude Code uses .claude/skills/doomer-review/

For an active agent listed above, if its skill or required reference files are missing,
continue the normal task and report the missing installation. Do not infer
permission to reinstall it. Otherwise, before each user turn that will edit
repository files, read its installed doomer-review skill and
references/install.md from disk. Follow the skill for documentation, configuration,
tests, and application code, including small edits and requests that do not
explicitly mention review.

Run the skill's local CLI version and managed-installation check once per
editing turn before making changes. Use the current on-disk pin and the skill's
saved approval decisions. A version remembered from an earlier turn or a
previously completed setup does not satisfy this check. Keep the online skill
update check scoped to once per coding session as the skill directs.

Follow the skill's setup and review workflow. Reuse valid repository consent
and verified hook activation. Keep repository upload consent and agent hook
trust as user choices, preserve a paused watcher, and honor explicit skip
decisions and the skill's read-only and skill-maintenance exemptions.

Use trusted hooks to capture changes and deliver review results. Investigate
feedback, fix confirmed issues within the user's scope, and record responses
as the hooks direct. Follow the skill's completion gate for the current
revision and include the complete Doomer review link when a review is delivered,
including a review with no findings.
<!-- doomer-managed:end -->
<!-- doomer-review-policy:end -->
