#!/bin/sh
# Removes what install.sh installed. Deliberately does NOT touch
# /etc/fleetconnect/config.yaml or /var/log/fleetconnect — same "leave the
# live credential in place, revoke it via the Credentials API instead"
# policy as preremove.sh (§11).
set -e

if command -v systemctl >/dev/null 2>&1 && [ -d /run/systemd/system ]; then
    systemctl stop fleetconnect || true
    systemctl disable fleetconnect || true
    rm -f /etc/systemd/system/fleetconnect.service
    systemctl daemon-reload || true
fi

rm -f /usr/bin/fleetconnect
