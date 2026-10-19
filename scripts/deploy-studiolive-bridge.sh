#!/usr/bin/env bash
#
# Install integrations/studiolive/bridge/osc_tcp1_bridge_jack.py on the
# StudioLive Mac mini and restart studiolive-bridge.
#
# The mixer user has no passwordless sudo, so the restart is triggered by
# SIGKILL plus the unit's Restart=on-failure. systemd 255 propagates that
# restart to studiolive-osc (Requires=), so Open Stage Control restarts too
# and connected browsers reconnect. Do not run during a show.
#
# Usage: ./scripts/deploy-studiolive-bridge.sh [user@host]
#
set -euo pipefail

HOST="${1:-mixer@studiolive}"
ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SRC="$ROOT_DIR/integrations/studiolive/bridge/osc_tcp1_bridge_jack.py"

scp -q "$SRC" "$HOST:studiolive/osc_tcp1_bridge_jack.py.new"
ssh "$HOST" 'set -e
cd ~/studiolive
/home/mixer/.venvs/osclive/bin/python -m py_compile osc_tcp1_bridge_jack.py.new
rm -rf __pycache__
if cmp -s osc_tcp1_bridge_jack.py.new osc_tcp1_bridge_jack.py; then
	rm osc_tcp1_bridge_jack.py.new
	echo "bridge unchanged; not restarting"
	exit 0
fi
cp osc_tcp1_bridge_jack.py "osc_tcp1_bridge_jack.py.bak-$(date +%Y%m%d-%H%M%S)"
mv osc_tcp1_bridge_jack.py.new osc_tcp1_bridge_jack.py
OLD=$(systemctl show studiolive-bridge -p MainPID --value)
kill -KILL "$OLD"
for _ in $(seq 1 30); do
	sleep 1
	NEW=$(systemctl show studiolive-bridge -p MainPID --value)
	STATE=$(systemctl show studiolive-bridge -p ActiveState --value)
	if [ "$NEW" != 0 ] && [ "$NEW" != "$OLD" ] && [ "$STATE" = active ]; then
		break
	fi
done
sleep 2
for u in studiolive-bridge studiolive-osc; do
	printf "%s: %s pid %s\n" "$u" "$(systemctl show $u -p ActiveState --value)" "$(systemctl show $u -p MainPID --value)"
done
journalctl -u studiolive-bridge --since "-40s" --no-pager -o cat | tail -5'
