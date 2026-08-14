#!/usr/bin/env bash
# One command: hostnames + sticky ports + TLS routing + both dev servers.
#   ./dev.sh
# Then open https://coolwebsite.test — the api answers at
# https://api.coolwebsite.test.
set -euo pipefail
cd "$(dirname "$0")"

export GERRY_API=${GERRY_API:-http://127.0.0.1:4780}
GERRY=${GERRY_BIN:-gerry}
command -v "$GERRY" >/dev/null || GERRY="$(dirname "$0")/../../gerry"

# Claim hostnames + routes (idempotent; creates the zone on first run).
"$GERRY" up -f gerrymander.yaml

# Sticky ports: same owner → same port, every run, forever. Safe to put in
# configs, bookmarks, and muscle memory.
API_PORT=$("$GERRY" port --owner coolwebsite/api -q)
WEB_PORT=$("$GERRY" port --owner coolwebsite/frontend -q)
echo "api  → 127.0.0.1:${API_PORT}  (https://api.coolwebsite.test)"
echo "web  → 127.0.0.1:${WEB_PORT}  (https://coolwebsite.test)"

cleanup() { kill 0 2>/dev/null || true; }
trap cleanup EXIT INT TERM

(cd backend && exec uv run uvicorn main:app --host 127.0.0.1 --port "$API_PORT" --reload) &
(cd frontend && [ -d node_modules ] || npm install --silent; PORT="$WEB_PORT" exec npm run dev) &

wait
