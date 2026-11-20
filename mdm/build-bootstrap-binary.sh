#!/usr/bin/env bash
set -euo pipefail

[[ $# == 1 ]] || { echo "Usage: $0 OUTPUT_DIR" >&2; exit 2; }
dir=$1
mkdir -p "$dir"
root="$(cd "$(dirname "$0")/.." && pwd)"
dir="$(cd "$dir" && pwd)"
for arch in arm64 amd64; do
  CGO_ENABLED=0 GOOS=darwin GOARCH="$arch" go -C "$root" build -trimpath -o "$dir/doomer-$arch" ./cmd/mdm-bootstrap
done
bash "$root/mdm/combine-binaries.sh" "$dir"
