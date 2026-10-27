#!/usr/bin/env bash
# Install soluterminal as a systemd --user unit.
# This auto-starts solud on login and restarts it if it crashes.
#
# Sessions persist across daemon restarts: each pane runs in its own
# Setsid'd agent subprocess, so stopping/restarting the service keeps
# your shells and Claude sessions alive — the next daemon reattaches
# to them automatically.
#
# To enable the cloud relay, create ~/.solu/service.env from
# systemd/service.env.example and restart the service.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")" && pwd)"
UNIT_SRC="$ROOT/systemd/soluterminal.service"
UNIT_DST="$HOME/.config/systemd/user/soluterminal.service"

if [ ! -x "$ROOT/solud" ]; then
  echo "error: $ROOT/solud not found. Run ./build.sh first."
  exit 1
fi

mkdir -p "$(dirname "$UNIT_DST")"
cp "$UNIT_SRC" "$UNIT_DST"

# Point the unit at the actual binary location (in case the repo was moved).
sed -i "s|%h/wsl-projects/soluterminal/solud|$ROOT/solud|" "$UNIT_DST"

systemctl --user daemon-reload
# Clear any prior `failed` state (e.g. a tripped start-limit from a bad
# release) so a reinstall actually brings the unit back up instead of
# silently refusing to start.
systemctl --user reset-failed soluterminal.service 2>/dev/null || true
systemctl --user enable --now soluterminal.service
echo ""
echo "✓ installed systemd --user unit: $UNIT_DST"
echo ""
echo "Useful commands:"
echo "  journalctl --user -u soluterminal -f    # tail logs"
echo "  systemctl --user status soluterminal    # status"
echo "  systemctl --user restart soluterminal   # restart"
echo "  systemctl --user stop soluterminal      # stop"
echo "  systemctl --user disable --now soluterminal  # remove from startup"
echo "  systemctl --user reset-failed soluterminal   # clear a tripped crash-loop limit"
echo ""
echo "To enable the cloud relay for this machine:"
echo "  cp $ROOT/systemd/service.env.example ~/.solu/service.env"
echo "  chmod 600 ~/.solu/service.env"
echo "  # edit ~/.solu/service.env and fill in your token + backend id (wsl or host), then:"
echo "  systemctl --user restart soluterminal"
echo ""
echo "On WSL2, you may need:  sudo loginctl enable-linger $USER"
echo "so the service keeps running after your shell session ends."
