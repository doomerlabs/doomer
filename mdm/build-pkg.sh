#!/bin/bash
set -euo pipefail
# Payload directories must be traversable by the managed user after installation.
umask 022

if [[ $# != 3 ]]; then
  echo "Usage: $0 DOOMER_MAC_BINARY codex|claude|codex,claude OUTPUT.pkg" >&2
  exit 2
fi
if [[ "$(uname -s)" != Darwin ]]; then
  echo "Build the macOS installer on macOS." >&2
  exit 2
fi

doomer_binary=$1
agents=$2
output=$3
case "$agents" in codex|claude|codex,claude|claude,codex) ;; *) exit 2 ;; esac
source_dir="$(cd "$(dirname "$0")/.." && pwd)"
stage="$(mktemp -d)"
trap 'rm -rf "$stage"' EXIT
runtime="$stage/root/usr/local/libexec/doomer-mdm"
mkdir -p "$runtime" "$stage/scripts" "$stage/root/Library/LaunchAgents"
install -m 0755 "$doomer_binary" "$runtime/doomer"
install -m 0644 "$source_dir/mdm/com.doomerlabs.doomer.mdm.enroll.plist" "$stage/root/Library/LaunchAgents/"

if [[ ",$agents," == *,claude,* ]]; then
  claude_dir="$stage/root/Library/Application Support/ClaudeCode/managed-settings.d"
  mkdir -p "$claude_dir"
  "$runtime/doomer" mdm agent-config --agent claude > "$claude_dir/50-doomer.json"
fi
if [[ ",$agents," == *,codex,* ]]; then
  mkdir -p "$stage/root/private/etc/codex/rules"
  install -m 0644 "$source_dir/mdm/doomer-mdm.rules" "$stage/root/private/etc/codex/rules/"
  cat > "$stage/scripts/postinstall" <<'SCRIPT'
#!/bin/bash
set -euo pipefail
/usr/bin/install -d -m 0755 /etc/codex
runtime=/usr/local/libexec/doomer-mdm
"$runtime/doomer" mdm agent-config --agent codex --merge-codex /etc/codex/hooks.json
SCRIPT
  chmod 0755 "$stage/scripts/postinstall"
fi

version="$("$runtime/doomer" --version | awk '{print $2}')"
[[ "$version" =~ ^20[0-9]{2}\.[0-9]{1,2}\.[0-9]{1,2}(\.[1-9][0-9]*)?(-beta\.[0-9]+)?$ ]] || {
  echo "The Doomer binary must report a release version." >&2
  exit 1
}
/usr/bin/pkgbuild --root "$stage/root" --scripts "$stage/scripts" \
  --identifier com.doomerlabs.doomer.mdm --version "$version" \
  --install-location / "$stage/component.pkg"

/usr/bin/productbuild --synthesize --product "$source_dir/mdm/requirements.plist" \
  --package "$stage/component.pkg" "$stage/Distribution"
product_args=(--distribution "$stage/Distribution" --package-path "$stage"
  --identifier com.doomerlabs.doomer.mdm --version "$version")
if [[ -n "${DOOMER_PKG_SIGN_IDENTITY:-}" ]]; then
  product_args+=(--sign "$DOOMER_PKG_SIGN_IDENTITY" --timestamp)
  if [[ -n "${DOOMER_PKG_SIGN_KEYCHAIN:-}" ]]; then
    product_args+=(--keychain "$DOOMER_PKG_SIGN_KEYCHAIN")
  fi
fi
/usr/bin/productbuild "${product_args[@]}" "$output"
