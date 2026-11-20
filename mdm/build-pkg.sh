#!/bin/bash
set -euo pipefail
umask 022

if [[ $# != 4 ]]; then
  echo "Usage: $0 bootstrap|runtime DOOMER_MAC_BINARY codex|claude|codex,claude OUTPUT.pkg" >&2
  exit 2
fi
if [[ "$(uname -s)" != Darwin ]]; then
  echo "Build the macOS installer on macOS." >&2
  exit 2
fi

kind=$1
doomer_binary=$2
agents=$3
output=$4
case "$kind" in bootstrap|runtime) ;; *) exit 2 ;; esac
case "$agents" in codex|claude|codex,claude|claude,codex) ;; *) exit 2 ;; esac
source_dir="$(cd "$(dirname "$0")/.." && pwd)"
stage="$(mktemp -d)"
trap 'rm -rf "$stage"' EXIT
mkdir -p "$stage/root"
identifier="com.doomerlabs.doomer.mdm.$kind"
location=/
script_args=()

if [[ "$kind" == runtime ]]; then
  # The updater alone promotes this payload after Installer returns success.
  location=/Library/DoomerMDM/incoming
  install -m 0755 "$doomer_binary" "$stage/root/doomer"
else
  runtime="$stage/root/Library/DoomerMDM"
  hook_dir="$stage/root/usr/local/libexec/doomer-mdm"
  mkdir -p "$runtime/incoming" "$hook_dir" "$stage/scripts" \
    "$stage/root/Library/LaunchAgents" "$stage/root/Library/LaunchDaemons"
  install -m 0755 "$doomer_binary" "$runtime/bootstrap-doomer"
  install -m 0755 "$source_dir/mdm/hook-launcher.sh" "$hook_dir/doomer"
  install -m 0755 "$source_dir/mdm/hook-launcher.sh" "$runtime/run-doomer"
  printf '%s\n' "${DOOMER_PKG_SIGN_IDENTITY:-}" > "$runtime/installer-identity"
  install -m 0644 "$source_dir/mdm/com.doomerlabs.doomer.mdm.enroll.plist" "$stage/root/Library/LaunchAgents/"
  install -m 0644 "$source_dir/mdm/com.doomerlabs.doomer.mdm.update.plist" "$stage/root/Library/LaunchDaemons/"

  postinstall="$stage/scripts/postinstall"
  cat > "$postinstall" <<'SCRIPT'
#!/bin/bash
set -euo pipefail
# An offline target loads the daemon when that volume first boots.
[[ "$3" == / ]] || exit 0
SCRIPT
  if [[ ",$agents," == *,claude,* ]]; then
    claude_dir="$stage/root/Library/Application Support/ClaudeCode/managed-settings.d"
    mkdir -p "$claude_dir"
    "$runtime/bootstrap-doomer" mdm agent-config --agent claude > "$claude_dir/50-doomer.json"
  fi
  if [[ ",$agents," == *,codex,* ]]; then
    mkdir -p "$stage/root/private/etc/codex/rules"
    install -m 0644 "$source_dir/mdm/doomer-mdm.rules" "$stage/root/private/etc/codex/rules/"
    cat >> "$postinstall" <<'SCRIPT'
/usr/bin/install -d -m 0755 /etc/codex
/Library/DoomerMDM/bootstrap-doomer mdm agent-config --agent codex --merge-codex /etc/codex/hooks.json
SCRIPT
  fi
  cat >> "$postinstall" <<'SCRIPT'
label=com.doomerlabs.doomer.mdm.update
if ! /bin/launchctl print "system/$label" >/dev/null 2>&1; then
  /bin/launchctl bootstrap system "/Library/LaunchDaemons/$label.plist"
fi
SCRIPT
  chmod 0755 "$postinstall"
  script_args=(--scripts "$stage/scripts")
fi

version="$("$doomer_binary" --version | awk '{print $2}')"
[[ "$version" =~ ^20[0-9]{2}\.[0-9]{1,2}\.[0-9]{1,2}(\.[1-9][0-9]*)?(-beta\.[0-9]+)?$ ]] || {
  echo "The Doomer binary must report a release version." >&2
  exit 1
}
/usr/bin/pkgbuild --root "$stage/root" ${script_args[@]+"${script_args[@]}"} \
  --ownership recommended --identifier "$identifier" --version "$version" \
  --install-location "$location" "$stage/component.pkg"

/usr/bin/productbuild --synthesize --product "$source_dir/mdm/requirements.plist" \
  --package "$stage/component.pkg" "$stage/Distribution"
product_args=(--distribution "$stage/Distribution" --package-path "$stage"
  --identifier "$identifier" --version "$version")
if [[ -n "${DOOMER_PKG_SIGN_IDENTITY:-}" ]]; then
  product_args+=(--sign "$DOOMER_PKG_SIGN_IDENTITY" --timestamp)
  if [[ -n "${DOOMER_PKG_SIGN_KEYCHAIN:-}" ]]; then
    product_args+=(--keychain "$DOOMER_PKG_SIGN_KEYCHAIN")
  fi
fi
/usr/bin/productbuild "${product_args[@]}" "$output"
