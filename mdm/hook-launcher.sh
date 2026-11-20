#!/bin/sh
# Both the system daemon and the existing hook path use the active runtime.
if [ -x /Library/DoomerMDM/doomer ]; then
  exec /Library/DoomerMDM/doomer "$@"
fi
exec /Library/DoomerMDM/bootstrap-doomer "$@"
