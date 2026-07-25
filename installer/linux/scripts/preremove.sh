#!/bin/sh
# Stops and disables the service before removal. Deliberately does NOT
# touch /etc/fleetconnect/config.yaml on either remove or purge — that
# file is never part of this package's contents (it's rendered by
# postinstall.sh, not shipped with a src:), so dpkg/rpm have no built-in
# conffile tracking for it at all; leaving no postremove step is what
# keeps it in place, not a config|noreplace declaration (§11).
set -e

systemctl stop fleetconnect || true
systemctl disable fleetconnect || true
