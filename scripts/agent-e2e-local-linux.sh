#!/usr/bin/env bash
# Populate exactly the Linux inputs verified by CI, from the local working tree.
set -euo pipefail
umask 077
root=$(git rev-parse --show-toplevel)
git_common=$(git rev-parse --path-format=absolute --git-common-dir)
image=doomer-agent-e2e:local
release=$(cat "$root/tests/codexinstall/baseline-release.txt")
if [[ ! $release =~ ^20[0-9]{2}\.[0-9]{1,2}\.[0-9]{1,2}\.[1-9][0-9]*$ ]]; then
  echo "Invalid E2E baseline release" >&2
  exit 1
fi
reports=${E2E_LOCAL_REPORTS:-/tmp/doomer-agent-e2e-linux}
mkdir -p "$reports"
if [[ ! -O $reports || -L $reports ]]; then
  echo "E2E reports must be an owned directory, not a symlink" >&2
  exit 1
fi
chmod 700 "$reports"
reports=$(cd "$reports" && pwd)
cache=${XDG_CACHE_HOME:-$HOME/.cache}/doomer-agent-e2e
mkdir -p "$cache/go-build" "$cache/go-mod" "$cache/agents"
chmod 700 "$cache" "$cache/go-build" "$cache/go-mod" "$cache/agents"
cache=$(cd "$cache" && pwd)
runner_home=$(mktemp -d "${TMPDIR:-/tmp}/doomer-agent-e2e-home.XXXXXX")
trap 'rm -rf -- "$runner_home"' EXIT
mkdir -p "$runner_home/.cache/go-build" "$runner_home/go/pkg/mod"
docker build --platform linux/amd64 -t "$image" -f "$root/tests/codexinstall/Dockerfile" "$root/tests/codexinstall"
docker run --rm --platform linux/amd64 --user "$(id -u):$(id -g)" \
  -v "$root:/src:ro" -v "$git_common:$git_common:ro" -v "$reports:/reports" \
  -v "$runner_home:/runner-home" -e HOME=/runner-home \
  -v "$cache/go-build:/runner-home/.cache/go-build" \
  -v "$cache/go-mod:/runner-home/go/pkg/mod" \
  -v "$cache/agents:/agent-cache" -e E2E_AGENT_CACHE=/agent-cache \
  -e E2E_CANDIDATE_RELEASE="$release" \
  -e TEST_OPENAI_API_KEY -e TEST_CLAUDE_CODE_OAUTH_TOKEN \
  -e E2E_R2_ACCOUNT_ID -e E2E_R2_BUCKET -e E2E_R2_ACCESS_KEY_ID -e E2E_R2_SECRET_ACCESS_KEY \
  -e E2E_CACHE_SIGNING_KEY -e E2E_CACHE_SIGNING_KEY_ID -e E2E_CACHE_PUBLIC_KEYS \
  -e E2E_SUITE_SCENARIOS \
  -e E2E_CLI_SMOKE_ONLY \
  "$image" bash -c '
    set -euo pipefail
    git config --global --add safe.directory /src
    if [[ ${E2E_CLI_SMOKE_ONLY:-} == 1 ]]; then
      CGO_ENABLED=0 go run -trimpath -buildvcs=false -ldflags=-buildid= ./tests/codexinstall \
        --cli-smoke --output /reports/cli-smoke.json
      exit
    fi
    export E2E_BASELINE_SOURCE="$HOME/baseline"
    scripts/agent-e2e-baseline.sh "$E2E_BASELINE_SOURCE"
    python3 scripts/agent-e2e-cache-plan.py --populate --jobs 8 --output /reports
  '
