#!/bin/bash
set -euo pipefail

# The workflow verifies and combines the binaries before exposing signing secrets.
: "${MDM_INSTALLER_P12_BASE64:?Missing installer signing certificate}"
: "${MDM_INSTALLER_P12_PASSWORD:?Missing installer signing certificate password}"
[[ $# == 3 ]] || { echo "Usage: $0 UNIVERSAL_DOOMER RUNTIME.pkg BOOTSTRAP.pkg" >&2; exit 2; }

umask 077
work="$(mktemp -d)"
keychain="$work/installer.keychain-db"
cleanup() {
  security delete-keychain "$keychain" >/dev/null 2>&1 || true
  rm -rf "$work"
}
trap cleanup EXIT
keychain_password="$(openssl rand -hex 24)"
printf '%s' "$MDM_INSTALLER_P12_BASE64" | /usr/bin/base64 -D > "$work/installer.p12"
security create-keychain -p "$keychain_password" "$keychain"
security unlock-keychain -p "$keychain_password" "$keychain"
security import "$work/installer.p12" -k "$keychain" -P "$MDM_INSTALLER_P12_PASSWORD" -T /usr/bin/productbuild >/dev/null
rm -f "$work/installer.p12"
unset MDM_INSTALLER_P12_BASE64 MDM_INSTALLER_P12_PASSWORD
installer_identity() {
  security find-identity -v "$keychain" | awk -F '"' '/"Developer ID Installer:/{ print $2 }'
}
identity="$(installer_identity)"
if [[ -z "$identity" ]]; then
  # A .p12 can omit Apple's intermediate. Import it without a trust override.
  curl --fail --silent --show-error --max-time 30 \
    https://www.apple.com/certificateauthority/DeveloperIDG2CA.cer -o "$work/DeveloperIDG2CA.cer"
  security import "$work/DeveloperIDG2CA.cer" -k "$keychain" >/dev/null
  identity="$(installer_identity)"
fi
security set-key-partition-list -S apple-tool:,apple: -s -k "$keychain_password" "$keychain" >/dev/null
export LC_ALL=C
[[ "$identity" =~ ^Developer\ ID\ Installer:\ .+\ \([A-Z0-9]{10}\)$ && "$identity" != *$'\n'* ]] || {
  echo "Expected one valid Developer ID Installer identity." >&2
  exit 1
}
/usr/bin/lipo "$1" -verify_arch arm64 x86_64
for kind in runtime bootstrap; do
  package=$2
  [[ "$kind" != bootstrap ]] || package=$3
  DOOMER_PKG_SIGN_IDENTITY="$identity" DOOMER_PKG_SIGN_KEYCHAIN="$keychain" \
    "$(dirname "$0")/build-pkg.sh" "$kind" "$1" codex,claude "$package"
  signature="$(/usr/sbin/pkgutil --check-signature "$package")"
  printf '%s\n' "$signature"
  grep -Eq '^[[:space:]]*Status: (signed by a certificate trusted by .+|signed by a developer certificate issued by Apple for distribution)[[:space:]]*$' <<< "$signature"
  leaf="$(sed -n 's/^[[:space:]]*1\. //p' <<< "$signature")"
  [[ "$leaf" == "$identity" ]]
done
