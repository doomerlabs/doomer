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
    if ! curl -fsSL --retry 3 --connect-timeout 15 --max-time 180 https://github.com/openai/codex/releases/download/rust-v0.167.0/codex-package-x86_64-unknown-linux-musl.tar.gz -o "$work/agent.tar.gz"; then
      # This is the same pinned rust-v0.167.0 asset through GitHub's asset API.
      # The release page can return gateway errors while the asset API works.
      curl -fsSL --retry 3 --connect-timeout 15 --max-time 180 -H 'Accept: application/octet-stream' \
        https://api.github.com/repos/openai/codex/releases/assets/715506641 -o "$work/agent.tar.gz"
    fi
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
if [[ -n ${E2E_CANDIDATE_SOURCE:-} ]]; then
  candidate_args+=(--candidate-source "$E2E_CANDIDATE_SOURCE")
fi
if [[ -n ${E2E_SKILLS_SOURCE:-} ]]; then
  candidate_args+=(--skills-source "$E2E_SKILLS_SOURCE")
else
  candidate_args+=(--generated-bootstrap)
fi
scenario_budget=0.35
if [[ $harness == codex && ( $scenario == upgrade-authorized-session || $scenario == policy-recovery || $scenario == policy-declined || $scenario == hooks-upgrade || $scenario == hooks-declined ) ]]; then
  # These scenarios include onboarding and additional approval/review turns.
  # Leave room for the final response reservation.
  scenario_budget=0.50
fi
run_result=0
"$work/runner" "${agent_args[@]}" "${candidate_args[@]}" \
  --local-root "$work/homes" --scenario "$scenario" --budget "$scenario_budget" --suite-budget 8.4 \
  --output "$reports/report.json" || run_result=$?
if [[ ${E2E_EXPECT_POLICY_REGRESSION:-0} == 1 ]]; then
  python3 - "$reports/report.json" <<'PY'
import json, sys
r = json.load(open(sys.argv[1]))
if r.get("passed") or r.get("error") != "deterministic prepare probe: missing policy after delivered offer" or not r.get("local_cleaned"):
    raise SystemExit("Baseline did not reproduce the expected missing-policy failure: " + r.get("error", ""))
print("Confirmed: released CLI fails the missing-policy recovery assertion; isolated fixture cleaned.")
PY
else
  exit "$run_result"
fi
