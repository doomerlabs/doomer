---
name: doomer-review
description: Use Doomer before repository edits and finish its review before reporting completion.
metadata:
  doomer-protocol: "2"
  doomer-update-policy: stable
---

# Doomer review

Before editing, run `doomer prepare --agent AGENT --repo . --prompt "USER TASK"`.
Replace AGENT with codex or claude and supply the actual task without shell
interpolation. Read the returned instructions and retain the task handle.
If a real harness session ID is available, pass it with `--session`.

Resume an existing task with `doomer prepare --task HANDLE` after interruption.
Never create a replacement task to recover an existing review. If that task's
selected runtime is missing, run `doomer task repair HANDLE` through the available
launcher, then resume the same handle. Repair restores that exact release; do not
substitute the current stable release for an active task. Follow structured
user-action requests. Never invent consent, account identity, or hook activation.
Reading this file directly is sufficient; native skill discovery is optional.

The selected CLI supplies its matching full instructions. Use its task commands
to submit changes, retrieve results, record every finding response, and verify
completion. A pending result or installed CLI does not establish a completed review.

If `doomer` is missing, read the official INSTALL.md at
https://raw.githubusercontent.com/doomerlabs/skills/b8611b37c4ef8ce47fd66b04af9166e047b9e0f3/INSTALL.md and require its SHA-256 to equal
`6f9566430e2f1646209daaa1e79a7ab1b93857bf6a35997085092eae996932a1`
before following its verified installation flow with the user's normal execution permissions.
