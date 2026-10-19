#!/usr/bin/env bash
#
# Regenerate the Open Stage Control session (v26 baseline plus the read-only
# ROOM tab) and install it on the StudioLive Mac mini as ..._v27.json.
#
# studiolive-osc.service still loads ..._v26.json, so on first install that
# path becomes a symlink to v27 and the original is kept as
# ..._v26.json.orig. Point the unit at v27 directly when convenient.
#
# Restart uses `sudo -n systemctl restart studiolive-osc.service` when the
# integrations/studiolive/sudoers/studiolive-dsp rule is installed, otherwise
# SIGKILL plus Restart=on-failure. Only Open Stage Control restarts; connected
# browsers reconnect. Do not run during a show.
#
# Usage: ./scripts/deploy-studiolive-osc.sh [user@host]
#
set -euo pipefail

HOST="${1:-mixer@studiolive}"
ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
DIR="$ROOT_DIR/integrations/studiolive"
OUT="$DIR/osc/StudioLive1602_OpenStageControl_v27.json"

python3 "$DIR/osc/add_room_tab.py" "$DIR/deployed/StudioLive1602_OpenStageControl_v26.json" "$OUT"
scp -q "$OUT" "$HOST:studiolive/StudioLive1602_OpenStageControl_v27.json.new"
ssh "$HOST" 'set -e
cd ~/studiolive
python3 -c "import json; json.load(open(\"StudioLive1602_OpenStageControl_v27.json.new\"))"
if [ -f StudioLive1602_OpenStageControl_v27.json ] && cmp -s StudioLive1602_OpenStageControl_v27.json.new StudioLive1602_OpenStageControl_v27.json; then
	rm StudioLive1602_OpenStageControl_v27.json.new
	echo "session unchanged; not restarting"
	exit 0
fi
mv StudioLive1602_OpenStageControl_v27.json.new StudioLive1602_OpenStageControl_v27.json
if [ ! -L StudioLive1602_OpenStageControl_v26.json ]; then
	mv StudioLive1602_OpenStageControl_v26.json StudioLive1602_OpenStageControl_v26.json.orig
	ln -s StudioLive1602_OpenStageControl_v27.json StudioLive1602_OpenStageControl_v26.json
fi
OLD=$(systemctl show studiolive-osc -p MainPID --value)
if sudo -n /usr/bin/systemctl restart studiolive-osc.service 2>/dev/null; then
	echo "restarted with sudo systemctl"
else
	echo "no sudoers rule; restarting via SIGKILL and Restart=on-failure"
	kill -KILL "$OLD"
fi
for _ in $(seq 1 30); do
	sleep 1
	NEW=$(systemctl show studiolive-osc -p MainPID --value)
	if [ "$NEW" != 0 ] && [ "$NEW" != "$OLD" ] && [ "$(systemctl show studiolive-osc -p ActiveState --value)" = active ]; then
		break
	fi
done
printf "studiolive-osc: %s pid %s\n" "$(systemctl show studiolive-osc -p ActiveState --value)" "$(systemctl show studiolive-osc -p MainPID --value)"'
