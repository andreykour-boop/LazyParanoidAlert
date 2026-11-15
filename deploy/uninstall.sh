#!/usr/bin/env bash
# Siren remover: stops and disables the service, removes the binary and the
# unit, keeps /opt/siren/config.json and the data/log directory for inspection.
#   sudo ./uninstall.sh
set -euo pipefail

DEST_DIR=/opt/siren
UNIT=/etc/systemd/system/siren.service

[ "$(id -u)" -eq 0 ] || { echo "run as root: sudo ./uninstall.sh"; exit 1; }

if systemctl list-unit-files siren.service --quiet 2>/dev/null; then
  systemctl disable --now siren 2>/dev/null || true
fi
rm -f "$UNIT" "$DEST_DIR/siren"
systemctl daemon-reload
echo ">> siren removed (config and logs kept in $DEST_DIR)."
echo ">> To wipe everything: sudo rm -rf $DEST_DIR"
