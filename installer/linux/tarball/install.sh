#!/bin/sh
# Installs Fleet Connector from a plain tarball, for minimal/non-systemd
# Linux environments the .deb/.rpm packages don't cover (§11). Run as root
# after extracting the tarball, from the directory this script lives in —
# it expects ./fleetconnect and ./fleetconnect.service alongside it:
#   tar xzf fleetconnect-linux-amd64.tar.gz
#   cd fleetconnect-linux-amd64
#   sudo ./install.sh
#
# Same install-time environment variables as the .deb/.rpm postinstall
# script (FLEETCONNECT_INSTALL_DESCRIPTION/_UPSTREAM/_NAME/_URL/_METADATA/
# _LOG_LEVEL, FLEETCONNECT_CREDFILE) produce the same config.yaml via the
# same gen-config code path — this script only differs in how the files
# land on disk and, if present, how the service is supervised.
set -e

script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)

install -d -m 0755 /etc/fleetconnect /var/log/fleetconnect
install -m 0755 "$script_dir/fleetconnect" /usr/bin/fleetconnect

if [ ! -f /etc/fleetconnect/config.yaml ] && [ -n "$FLEETCONNECT_INSTALL_DESCRIPTION" ]; then
    args="gen-config --path /etc/fleetconnect/config.yaml --description $FLEETCONNECT_INSTALL_DESCRIPTION --upstream $FLEETCONNECT_INSTALL_UPSTREAM"
    [ -n "$FLEETCONNECT_INSTALL_NAME" ] && args="$args --name $FLEETCONNECT_INSTALL_NAME"
    [ -n "$FLEETCONNECT_INSTALL_URL" ] && args="$args --url $FLEETCONNECT_INSTALL_URL"
    [ -n "$FLEETCONNECT_INSTALL_METADATA" ] && args="$args --metadata $FLEETCONNECT_INSTALL_METADATA"
    [ -n "$FLEETCONNECT_INSTALL_LOG_LEVEL" ] && args="$args --log-level $FLEETCONNECT_INSTALL_LOG_LEVEL"
    if [ -n "$FLEETCONNECT_CREDFILE" ]; then
        args="$args --authtoken file:$FLEETCONNECT_CREDFILE"
    fi
    # shellcheck disable=SC2086
    /usr/bin/fleetconnect $args
fi

# systemd, if present, is supervised the same way the .deb/.rpm path does
# it (§9/§11). A box with neither systemd nor any other supervisor in
# place is left with the binary and config installed but not started —
# there's no portable way to guess at every init system (SysV, OpenRC,
# runit, ...) this script might land on, so wiring the chosen one up is
# left to whatever's already managing services on that box.
if command -v systemctl >/dev/null 2>&1 && [ -d /run/systemd/system ]; then
    install -m 0644 "$script_dir/fleetconnect.service" /etc/systemd/system/fleetconnect.service
    systemctl daemon-reload
    systemctl enable --now fleetconnect || true
else
    echo "systemd not detected — fleetconnect is installed at /usr/bin/fleetconnect but not started."
    echo "Wire it into your own supervisor (init.d/OpenRC/runit/...), or run it directly:"
    echo "  FLEETCONNECT_CONFIG=/etc/fleetconnect/config.yaml /usr/bin/fleetconnect"
fi
