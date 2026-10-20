#!/bin/bash
# Reliable dashboard restart — kills old process, rebuilds, starts, verifies
DASHBOARD_DIR="$(cd "$(dirname "$0")" && pwd)"
BINARY="$DASHBOARD_DIR/pokegents-dashboard"
LOG="/tmp/pokegents-dashboard.log"
POKEGENTS_DATA="${POKEGENTS_DATA:-$HOME/.ccsession}"
PORT=$(jq -r '.port // 7834' "$POKEGENTS_DATA/config.json" 2>/dev/null || echo "7834")

# Kill existing
kill $(lsof -ti :$PORT) 2>/dev/null
sleep 1

# Rebuild Go binary
(cd "$DASHBOARD_DIR" && CGO_CFLAGS="-DSQLITE_ENABLE_FTS5" go build -o "$BINARY" . 2>&1) || true

# Rebuild frontend
(cd "$DASHBOARD_DIR/web" && npm run build 2>&1 | tail -1)

# Start with absolute path (immune to cwd changes)
"$BINARY" serve &>"$LOG" &
sleep 2

# Verify
if lsof -i :$PORT 2>/dev/null | grep -q LISTEN; then
  echo "Dashboard running on http://localhost:$PORT"
else
  echo "FAILED to start. Logs:"
  tail -5 "$LOG"
fi
