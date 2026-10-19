#!/usr/bin/env bash
#
# Copy the JACK-tagged jive-room binary (from `just build-studiolive`) to the
# StudioLive Mac mini and check that it links there. Touches no services.
#
# Usage: ./scripts/deploy-studiolive.sh [user@host]
#
set -euo pipefail

HOST="${1:-mixer@studiolive}"
ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BIN="$ROOT_DIR/jive-room"
REMOTE_DIR=".local/bin"

if [ ! -x "$BIN" ]; then
	echo "No $BIN. Run 'just build-studiolive' first." >&2
	exit 1
fi

ssh "$HOST" "mkdir -p ~/$REMOTE_DIR"
scp -q "$BIN" "$HOST:$REMOTE_DIR/jive-room.new"
ssh "$HOST" "set -e
cd ~/$REMOTE_DIR
if ldd jive-room.new | grep -q 'not found'; then
	ldd jive-room.new | grep 'not found' >&2
	rm -f jive-room.new
	echo 'jive-room has unresolved libraries on this host; not installed.' >&2
	exit 1
fi
mv jive-room.new jive-room
echo \"installed \$HOME/$REMOTE_DIR/jive-room\"
ldd jive-room | grep -E 'jack|libc\\.so'"
