#!/usr/bin/env bash
# Populate exactly the Linux inputs verified by CI, from the local working tree.
set -euo pipefail
root=$(git rev-parse --show-toplevel)
image=doomer-agent-e2e:local
reports=${E2E_LOCAL_REPORTS:-/tmp/doomer-agent-e2e-linux}
mkdir -p "$reports"
chmod 777 "$reports"
docker build --platform linux/amd64 -t "$image" -f "$root/tests/codexinstall/Dockerfile" "$root/tests/codexinstall"
docker run --rm --platform linux/amd64 \
  -v "$root:/src:ro" -v "$reports:/reports" \
  -v doomer-e2e-phase3-go-build:/home/runner/.cache/go-build \
  -v doomer-e2e-phase3-go-mod:/home/runner/go/pkg/mod \
  -e TEST_OPENAI_API_KEY -e TEST_CLAUDE_CODE_OAUTH_TOKEN \
  -e E2E_R2_ACCOUNT_ID -e E2E_R2_BUCKET -e E2E_R2_ACCESS_KEY_ID -e E2E_R2_SECRET_ACCESS_KEY \
  -e E2E_CACHE_SIGNING_KEY -e E2E_CACHE_SIGNING_KEY_ID -e E2E_CACHE_PUBLIC_KEYS \
  -e E2E_SUITE_SCENARIOS \
  "$image" bash -c '
    set -euo pipefail
    export E2E_BASELINE_SOURCE="$HOME/baseline"
    scripts/agent-e2e-baseline.sh "$E2E_BASELINE_SOURCE"
    export E2E_CANDIDATE_RELEASE=$(curl -fsSL https://downloads.doomer.ai/cli/stable.json | python3 -c '\''import json,sys;print(json.load(sys.stdin)["version"])'\'')
    python3 scripts/agent-e2e-cache-plan.py --populate --jobs 8 --output /reports
  '
