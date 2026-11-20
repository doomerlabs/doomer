#!/usr/bin/env bash
set -euo pipefail

: "${GITHUB_REPOSITORY:?}"
: "${GITHUB_REF_NAME:?}"
[[ $# == 1 ]] || { echo "Usage: $0 OUTPUT_DIR" >&2; exit 2; }
tag="$GITHUB_REF_NAME"
[[ "$tag" =~ ^20[0-9]{2}\.[0-9]{1,2}\.[0-9]{1,2}(\.[1-9][0-9]*)?(-beta\.[0-9]+)?$ ]] || exit 2
commit="$(git rev-parse HEAD)"

# Allow time for signing, the native verifier matrix, and runner scheduling.
deadline=$((SECONDS + 120 * 60))
while ((SECONDS < deadline)); do
  runs="$(gh api --method GET --paginate "repos/$GITHUB_REPOSITORY/actions/workflows/mdm-release.yml/runs" \
    -f event=push -f "head_sha=$commit" -f per_page=100 \
    --jq ".workflow_runs[] | select(.display_title == \"Sign MDM installer $tag\") | [.id, .status, .conclusion] | @tsv")"
  if [[ -n "$runs" ]]; then
    [[ "$runs" != *$'\n'* ]] || { echo "Multiple signing runs for the release tag" >&2; exit 1; }
    read -r run_id state conclusion <<< "$runs"
    if [[ "$state" == completed ]]; then
      [[ "$conclusion" == success ]] || {
        echo "MDM signing run $run_id finished with $conclusion" >&2
        exit 1
      }
      gh run download "$run_id" --repo "$GITHUB_REPOSITORY" --name "mdm-$tag" --dir "$1"
      exit 0
    fi
  fi
  sleep 15
done
echo "Timed out waiting for the signed MDM installer for $tag" >&2
exit 1
