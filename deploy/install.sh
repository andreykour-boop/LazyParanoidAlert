#!/usr/bin/env bash
# Siren installer: Linux (amd64 or arm64) + systemd.
#
#   sudo ./install.sh
#
# First run: puts the binary into /opt/siren, creates config.json from the
# example (edit it before the first start!), installs the systemd unit.
# Re-run: updates binary and unit, restarts the service if it is running.
#
# EXPECTED_ARCH is the machine architecture this kit was built for:
# x86_64 (amd64 kit) or aarch64 (arm64 kit). The installer refuses to run
# on any other machine instead of installing a binary the kernel cannot
# execute.
set -euo pipefail

EXPECTED_ARCH="x86_64"
DEST_DIR=/opt/siren
UNIT=/etc/systemd/system/siren.service
SRC_DIR="$(cd "$(dirname "$0")" && pwd)"

[ "$(id -u)" -eq 0 ] || { echo "run as root: sudo ./install.sh"; exit 1; }
[ "$(uname -m)" = "$EXPECTED_ARCH" ] || { echo "unsupported architecture: $(uname -m), this kit needs $EXPECTED_ARCH"; exit 1; }
command -v systemctl >/dev/null || { echo "systemd is required"; exit 1; }

# Disk health watch needs smartmontools (smartctl) and nvme-cli for NVMe
# drives. The agent keeps working without them (the missing tool is reported
# honestly in every report), but installing them here makes the watch fully
# functional out of the box. On small ARM boards the tools may exist but the
# hardware behind them may not; the agent reports that honestly too.
install_health_tools() {
  if command -v apt-get >/dev/null; then
    apt-get update -qq || true
    DEBIAN_FRONTEND=noninteractive apt-get install -y -qq smartmontools nvme-cli || \
      { echo ">> warning: apt install of smartmontools/nvme-cli failed; install them manually"; }
  elif command -v dnf >/dev/null; then
    dnf install -y -q smartmontools nvme-cli || \
      { echo ">> warning: dnf install failed; install smartmontools/nvme-cli manually"; }
  elif command -v yum >/dev/null; then
    yum install -y -q smartmontools nvme-cli || \
      { echo ">> warning: yum install failed; install smartmontools/nvme-cli manually"; }
  elif command -v zypper >/dev/null; then
    zypper --non-interactive install smartmontools nvme-cli || \
      { echo ">> warning: zypper install failed; install smartmontools/nvme-cli manually"; }
  elif command -v pacman >/dev/null; then
    pacman -Sy --noconfirm --needed smartmontools nvme-cli || \
      { echo ">> warning: pacman install failed; install them manually"; }
  else
    echo ">> unknown package manager: install smartmontools and nvme-cli manually"
  fi
}
install_health_tools

mkdir -p "$DEST_DIR"
install -m 0755 "$SRC_DIR/siren" "$DEST_DIR/siren"

fresh_config=0
if [ ! -f "$DEST_DIR/config.json" ]; then
  install -m 0600 "$SRC_DIR/config.example.json" "$DEST_DIR/config.json"
  fresh_config=1
fi

# Sanity: refuse to start with the placeholder token still in place.
if grep -q "REPLACE-WITH-BOT-TOKEN" "$DEST_DIR/config.json"; then
  echo ">> config $DEST_DIR/config.json still has the placeholder token."
  echo ">> Edit it (token and chat_id of your Telegram bot), then:"
  echo ">>   sudo systemctl enable --now siren"
  install -m 0644 "$SRC_DIR/siren.service" "$UNIT"
  systemctl daemon-reload
  echo ">> binary and unit installed; service NOT started (config is a template)."
  exit 0
fi

install -m 0644 "$SRC_DIR/siren.service" "$UNIT"
systemctl daemon-reload

if [ "$fresh_config" -eq 0 ] && systemctl is-active --quiet siren; then
  systemctl restart siren
  echo ">> binary/unit updated, service restarted"
else
  systemctl enable --now siren
  echo ">> service enabled and started"
fi

sleep 1
systemctl --no-pager --lines=3 status siren || true
echo
echo "Journal:  journalctl -u siren -f"
echo "Remove:   sudo ./uninstall.sh"
