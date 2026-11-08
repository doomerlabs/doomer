#!/usr/bin/env bash
# Run the full real-agent suite in disposable homes on the Depot runner.
set -euo pipefail
harness=${1:?expected codex or claude}
scenario=${2:-all}
reports=${E2E_REPORT_DIR:?expected E2E_REPORT_DIR}
mkdir -p "$reports"
printf '%s\n' "${DEPOT_JOB_URL:-}" > "$reports/ci-job-url.txt"
work=$(mktemp -d "${HOME}/doomer-agent-e2e.XXXXXX")
trap 'rm -rf "$work"' EXIT
mkdir -p "$work/homes" "$work/agent"
case "$harness" in
  codex)
    : "${TEST_OPENAI_API_KEY:?Codex API test credential is required}"
    unset TEST_CLAUDE_CODE_OAUTH_TOKEN TEST_ANTHROPIC_API_KEY ANTHROPIC_API_KEY ANTHROPIC_AUTH_TOKEN
    curl -fsSL --retry 3 https://github.com/openai/codex/releases/download/rust-v0.167.0/codex-package-x86_64-unknown-linux-musl.tar.gz -o "$work/agent.tar.gz"
    echo "3bbf75e83de6b58aed28f2cf6e33ad423ec31fd4700a1457b94be557ea5a7c6e  $work/agent.tar.gz" | sha256sum -c -
    tar -xzf "$work/agent.tar.gz" -C "$work/agent"
    agent_args=(--harness codex --auth api --local-codex "$work/agent/bin/codex")
    ;;
  claude)
    : "${TEST_CLAUDE_CODE_OAUTH_TOKEN:?Claude OAuth test credential is required; API fallback is forbidden}"
    unset TEST_OPENAI_API_KEY TEST_ANTHROPIC_API_KEY ANTHROPIC_API_KEY ANTHROPIC_AUTH_TOKEN ANTHROPIC_BASE_URL
    curl -fsSL --retry 3 https://downloads.claude.ai/claude-code-releases/2.1.319/linux-x64/claude -o "$work/agent/claude"
    echo "dabafec82ca1319fa43d8be36a10fc61b1d11821cb98ad5df04ad95e199b8eed  $work/agent/claude" | sha256sum -c -
    chmod 700 "$work/agent/claude"
    agent_args=(--harness claude --auth subscription --local-claude "$work/agent/claude")
    ;;
  *) echo 'Expected codex or claude' >&2; exit 1 ;;
esac
go build -o "$work/runner" ./tests/codexinstall
# The published version labels the fixture; source comes from the checked-out CLI.
release=${E2E_CANDIDATE_RELEASE:?expected the published release from the CI plan job}
if [[ ! $release =~ ^20[0-9]{2}\.[0-9]{1,2}\.[0-9]{1,2}\.[1-9][0-9]*$ ]]; then
  echo "Invalid Doomer release supplied by the CI plan job" >&2
  exit 1
fi
candidate_args=(--candidate-release "$release")
if [[ -n ${E2E_SKILLS_SOURCE:-} ]]; then
  candidate_args+=(--skills-source "$E2E_SKILLS_SOURCE")
else
  candidate_args+=(--generated-bootstrap)
fi
"$work/runner" "${agent_args[@]}" "${candidate_args[@]}" \
  --local-root "$work/homes" --scenario "$scenario" --budget 0.35 --suite-budget 8.4 \
  --output "$reports/report.json"
