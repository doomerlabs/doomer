#!/bin/sh
# The system daemon and agent hooks share the active runtime.
if [ -x /Library/DoomerMDM/doomer ]; then
  exec /Library/DoomerMDM/doomer "$@"
fi
case "${1-}:${2-}" in
  mdm:update) exec /Library/DoomerMDM/bootstrap-updater "$@" ;;
  mdm:hook|mdm:enroll) printf '%s\n' '{}' ;;
  *) echo 'Managed Doomer is waiting for its first verified runtime.' >&2; exit 1 ;;
esac
