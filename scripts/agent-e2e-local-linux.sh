#!/usr/bin/env bash
set -euo pipefail
umask 077
root=$(git rev-parse --show-toplevel)
runner=${E2E_RUNNER_SOURCE:-$root/../agent-e2e}
if [[ ! -f $runner/scripts/run.py ]]; then
  echo 'Clone doomerlabs/agent-e2e alongside this checkout or set E2E_RUNNER_SOURCE.' >&2
  exit 1
fi
candidate=(--cli-source "$root")
if [[ -n ${E2E_CANDIDATE_BINARY:-} ]]; then
  candidate=(--cli-binary "$E2E_CANDIDATE_BINARY")
elif [[ ${E2E_BUILD_CANDIDATE:-0} != 1 ]]; then
  echo 'Set E2E_CANDIDATE_BINARY to a precompiled CLI. Users may explicitly opt into building with E2E_BUILD_CANDIDATE=1; agents must not.' >&2
  exit 1
fi
exec python3 "$runner/scripts/run.py" --linux --changed-repo "$root" "${candidate[@]}" "$@"
