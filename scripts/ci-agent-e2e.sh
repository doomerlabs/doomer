#!/usr/bin/env bash
# Run the full real-agent suite in disposable homes on the Depot runner.
set -euo pipefail
harness=${1:?expected codex or claude}
scenario=${2:-all}
reports=${E2E_REPORT_DIR:?expected E2E_REPORT_DIR}
mkdir -p "$reports"
printf '%s\n' "${DEPOT_JOB_URL:-}" > "$reports/ci-job-url.txt"
export GOTOOLCHAIN=local CGO_ENABLED=1
work=$(mktemp -d "${HOME}/doomer-agent-e2e.XXXXXX")
trap 'rm -rf "$work"' EXIT
mkdir -p "$work/homes" "$work/agent"

# Cached archives are accepted only after checking the same pinned digest used
# for a fresh download. Publish atomically so concurrent runs never see a part.
fetch_agent() {
  local url=$1 digest=$2 target=$3 fallback=${4:-} cached='' staged=''
  if [[ -n ${E2E_AGENT_CACHE:-} ]]; then
    mkdir -p "$E2E_AGENT_CACHE"
    cached="$E2E_AGENT_CACHE/$digest"
    if [[ -f $cached ]] && printf '%s  %s\n' "$digest" "$cached" | sha256sum -c --status; then
      cp "$cached" "$target" || return 1
      echo "Reused verified agent download: $digest"
      return
    fi
  fi
  if ! curl -fsSL --retry 2 --connect-timeout 15 --max-time 600 "$url" -o "$target"; then
    [[ -n $fallback ]] || return 1
    curl -fsSL --retry 2 --connect-timeout 15 --max-time 600 -H 'Accept: application/octet-stream' "$fallback" -o "$target" || return 1
  fi
  printf '%s  %s\n' "$digest" "$target" | sha256sum -c - || return 1
  if [[ -n $cached ]]; then
    staged=$(mktemp "$E2E_AGENT_CACHE/.agent.XXXXXX") || return 1
    if cp "$target" "$staged" && chmod 600 "$staged" && mv -f "$staged" "$cached"; then
      :
    else
      rm -f "$staged"
      return 1
    fi
  fi
}
if ! command -v aws >/dev/null 2>&1 && [[ -n ${E2E_R2_ACCOUNT_ID:-} ]]; then
  curl -fsSL --retry 3 https://awscli.amazonaws.com/awscli-exe-linux-x86_64-2.17.44.zip -o "$work/aws.zip"
  echo "ed46356f6358c0dfa30416899bf3efe0001689520ffce22c128f5bc617e8f4c3  $work/aws.zip" | sha256sum -c -
  unzip -q "$work/aws.zip" -d "$work"
  "$work/aws/install" --install-dir "$work/aws-cli" --bin-dir "$work/bin"
  export PATH="$work/bin:$PATH"
fi
case "$harness" in
  codex)
    if [[ ${E2E_CACHE_ONLY:-0} != 1 ]]; then : "${TEST_OPENAI_API_KEY:?Codex API test credential is required}"; fi
    unset TEST_CLAUDE_CODE_OAUTH_TOKEN TEST_ANTHROPIC_API_KEY ANTHROPIC_API_KEY ANTHROPIC_AUTH_TOKEN
    fetch_agent https://github.com/openai/codex/releases/download/rust-v0.167.0/codex-package-x86_64-unknown-linux-musl.tar.gz \
      3bbf75e83de6b58aed28f2cf6e33ad423ec31fd4700a1457b94be557ea5a7c6e "$work/agent.tar.gz" \
      https://api.github.com/repos/openai/codex/releases/assets/715506641
    tar -xzf "$work/agent.tar.gz" -C "$work/agent"
    agent_args=(--harness codex --auth api --local-codex "$work/agent/bin/codex")
    ;;
  claude)
    if [[ ${E2E_CACHE_ONLY:-0} != 1 ]]; then : "${TEST_CLAUDE_CODE_OAUTH_TOKEN:?Claude OAuth test credential is required; API fallback is forbidden}"; fi
    unset TEST_OPENAI_API_KEY TEST_ANTHROPIC_API_KEY ANTHROPIC_API_KEY ANTHROPIC_AUTH_TOKEN ANTHROPIC_BASE_URL
    fetch_agent https://downloads.claude.ai/claude-code-releases/2.1.319/linux-x64/claude \
      dabafec82ca1319fa43d8be36a10fc61b1d11821cb98ad5df04ad95e199b8eed "$work/agent/claude"
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
# One contract for local population, CI preflight, and fallback execution.
# Subscription mode does not consume the API dollar budget.
scenario_budget=0.50
extra_args=(--jobs "${E2E_JOBS:-4}")
if [[ ${E2E_CACHE_ONLY:-0} == 1 ]]; then extra_args+=(--cache-only); fi
if [[ ${E2E_FORCE_RUN:-0} == 1 ]]; then extra_args+=(--no-cache); fi
if [[ ${E2E_EXPECT_POLICY_REGRESSION:-0} == 1 ]]; then extra_args+=(--expect-policy-regression); fi
if [[ -n ${E2E_SUITE_SCENARIOS:-} ]]; then extra_args+=(--suite-scenarios "$E2E_SUITE_SCENARIOS"); fi
run_result=0
"$work/runner" "${agent_args[@]}" "${candidate_args[@]}" \
  --local-root "$work/homes" --scenario "$scenario" --budget "$scenario_budget" --suite-budget 20 "${extra_args[@]}" \
  --output "$reports/report.json" || run_result=$?
if [[ ${E2E_EXPECT_POLICY_REGRESSION:-0} == 1 ]]; then
  if [[ $run_result != 0 && ${E2E_CACHE_ONLY:-0} == 1 ]]; then exit "$run_result"; fi
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
