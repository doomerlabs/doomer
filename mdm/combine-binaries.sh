#!/bin/bash
set -euo pipefail

inputs="${1:?Expected a directory with verified macOS binaries}"
/usr/bin/lipo -create "$inputs/doomer-arm64" "$inputs/doomer-amd64" -output "$inputs/doomer"
/usr/bin/lipo "$inputs/doomer" -verify_arch arm64 x86_64
