---
name: doomer-review
description: Use Doomer before repository edits and finish its review before reporting completion.
metadata:
{{metadata}}
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
https://raw.githubusercontent.com/doomerlabs/skills/5162fcf335422eb2a978fe67fd8b8f18538b68d8/INSTALL.md and require its SHA-256 to equal
`7acfef25b46abcb37d3361415ea80850f77cfb77812f5740cff6d9bcfe341191`
before following its verified installation flow with the user's normal execution permissions.
