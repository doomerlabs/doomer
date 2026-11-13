#!/usr/bin/env bash
set -euo pipefail
root=$(git rev-parse --show-toplevel)
runner=${E2E_RUNNER_SOURCE:-$root/../agent-e2e}
if [[ ! -f $runner/scripts/run.py ]]; then
  echo 'Clone doomerlabs/agent-e2e alongside this checkout or set E2E_RUNNER_SOURCE.' >&2
  exit 1
fi
candidate=(--cli-source "$root")
if [[ -n ${E2E_CANDIDATE_BINARY:-} ]]; then candidate=(--cli-binary "$E2E_CANDIDATE_BINARY"); fi
exec python3 "$runner/scripts/run.py" --linux --changed-repo "$root" "${candidate[@]}" "$@"
