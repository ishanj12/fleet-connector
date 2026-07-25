#!/bin/sh
# Runs after the package installs. If config.yaml doesn't already exist
# (e.g. ops staged gen-config output ahead of install — §11's preferred
# path), and install-time properties were supplied as environment
# variables by whatever drove this install (Ansible/Puppet/Chef, a scripted
# apt/yum install, etc — §11), render it via gen-config directly — the
# same config-construction/validation code the wizard uses.
set -e

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

systemctl daemon-reload
systemctl enable --now fleetconnect || true
