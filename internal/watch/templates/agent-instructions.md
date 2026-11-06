## Doomer code review

Before each editing turn, read the installed doomer-review SKILL.md and its
references/install.md. Codex uses .agents/skills/doomer-review/ and Claude uses
.claude/skills/doomer-review/, including Cowork and remote sessions. If the skill
is absent from the catalog, read those files through the tool that reaches the
checkout. Run the skill's checks in the environment that edits the files and
report observed coverage failures before editing, following its permission and
recovery rules, explicit skips, and skill-maintenance exemptions. Do not infer
an unsupported runtime without checking.

Doomer's trusted hooks capture tasks and edits and deliver review results;
do not poll review state or run setup on every edit. Investigate feedback
privately, fix confirmed issues
within the user's scope, and record each response using the hook's instructions.
Include the complete Doomer review link once in the final answer, including when
no issues remain. Only report completion when the hook confirms that revision.
Repository upload consent and coding-agent hook trust are separate user choices.
If setup is missing, follow the skill's onboarding instructions. Never approve
uploads or hook trust on the user's behalf. Preserve a paused watcher.
