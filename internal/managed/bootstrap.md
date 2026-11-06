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

Treat preparation as an edit gate. Wait for `state=ready`, `software_ready=true`,
`authenticated=true`, and `upload_approved=true` before the first repository edit.
If setup needs user input, ask before editing and resume the same task afterwards.
Keep requested work pending while preparation is blocked.

Run the CLI in the environment that edits the checkout. In Cowork or another
environment with a temporary home, enable portable setup before editing.
Its browser approval explains how to retain account access and this repository's
grant in private Git metadata so later sessions can reuse them. The CLI restores
approved portable setup itself. Never copy tokens or rewrite grant files.
The initial prepare command above works with older launchers. Before approving
setup or editing in a temporary-home environment, execute the returned
`portable_setup_command` argument array when present. It invokes the selected
runtime directly and resumes the same task with portable setup enabled.

If prepare reports a bootstrap upgrade, offer it to the user and apply it only
after acceptance. Keep the repository's update policy and any local edits.

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
https://raw.githubusercontent.com/doomerlabs/skills/6029750bef71c137dedd982a6aea79a26ede7b7a/INSTALL.md and require its SHA-256 to equal
`82d44c9402c629b94071aeaba3bc24184c8bed03413a2a354726e4405c9c535f`
before following its verified installation flow with the user's normal execution permissions.
Complete this prerequisite before editing. In Cowork, install into the editing
VM's personal environment, preserving any existing repository bootstrap.
