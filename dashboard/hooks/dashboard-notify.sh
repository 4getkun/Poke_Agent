#!/bin/bash
# Forward Claude Code hook events to the pokegent dashboard service.
# Registered alongside status-update.sh on all lifecycle events.
# Fire-and-forget: exits immediately, curl runs in background.

INPUT=$(cat)
DASHBOARD_URL="${POKEGENTS_DASHBOARD_URL:-http://localhost:7834}"

curl -s -m 1 -X POST "$DASHBOARD_URL/api/events" \
  -H "Content-Type: application/json" \
  -d "$INPUT" >/dev/null 2>&1 &

exit 0
