#!/usr/bin/env bash
# One command: hostnames + sticky ports + TLS routing + both dev servers.
#   ./dev.sh
# Then open https://coolwebsite.test — the api answers at
# https://api.coolwebsite.test.
#
# `gerry run` is the whole integration: it fetches the owner's sticky port,
# sets $PORT (and substitutes literal {PORT} in args), and execs your tool.
# Any runtime works the same way — uvicorn here, vite via bun or node, cargo
# watch, `go run`, rails… gerry never wraps or supervises your dev server.
set -euo pipefail
cd "$(dirname "$0")"

export GERRY_API=${GERRY_API:-http://127.0.0.1:4780}
GERRY=${GERRY_BIN:-gerry}
command -v "$GERRY" >/dev/null || GERRY="$(cd "$(dirname "$0")/../.." && pwd)/gerry"

# Idempotent safety net so the api hostname routes even if vite (whose
# plugin also applies the manifest) isn't started.
"$GERRY" up -f gerrymander.yaml >/dev/null

# The frontend needs NO gerry invocation at all: @gerrymander/vite (see
# frontend/vite.config.ts) claims the hostname + sticky port itself the
# moment vite boots. Plain `bun run dev` in frontend/ works standalone.
if command -v bun >/dev/null; then JS_INSTALL="bun install"; JS_DEV="bun run dev";
else JS_INSTALL="npm install --silent"; JS_DEV="npm run dev"; fi

cleanup() { kill 0 2>/dev/null || true; }
trap cleanup EXIT INT TERM

# Non-JS tools use `gerry run`, the port courier: sticky $PORT + {PORT} args.
(cd backend && exec "$GERRY" run --owner coolwebsite/api -- \
  uv run uvicorn main:app --host 127.0.0.1 --port '{PORT}' --reload) &

(cd frontend && [ -d node_modules ] || $JS_INSTALL; exec $JS_DEV) &

wait
