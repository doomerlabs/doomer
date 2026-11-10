#!/usr/bin/env bash
set -euo pipefail
# Keep the historical source outside the candidate repository's file inventory.
root=${1:?expected output directory}
mkdir -p "$root"
git -C "$root" init -q
git -C "$root" fetch --quiet --depth=1 https://github.com/doomerlabs/doomer.git 83f551519b8dcbc0786f9d2817a7df87656c60ff
git -C "$root" checkout --quiet --detach FETCH_HEAD
