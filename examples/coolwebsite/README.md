# coolwebsite — Python API + Vite React through gerrymander

The canonical "two hostnames, one project" dev setup:

| | |
|---|---|
| `https://coolwebsite.test` | Vite + React (gerrymander-vite — `bun run dev` is the whole interface), HMR over `wss://coolwebsite.test` |
| `https://backend.coolwebsite.test` | FastAPI via `gerry run` (uvicorn --reload), CORS to the frontend origin |

Renaming a hostname = edit `gerrymander.yaml`, restart the dev server: the
plugin claims the new label and releases the old one (this backend was
literally renamed from `api.` to `backend.` that way).

```sh
./dev.sh          # claims hostnames + sticky ports, starts both servers
open https://coolwebsite.test
```

No `/etc/hosts` edits, no port hunting, no TLS warnings, no proxy config.

## What each piece does

**`gerrymander.yaml`** declares the project: two services, each with
hostnames in the zone and a `port_pool` backend. `gerry up` (run by dev.sh):

1. creates the `coolwebsite.test` zone if it's the first run,
2. claims `@` and `api` — conflicts with anything else on the machine fail
   loudly here, not at 2am,
3. grants each service a **sticky port** (same owner → same port, forever —
   safe to write into configs), starting at 51000, away from the congested
   5173/8000/8080 range,
4. routes the TLS proxy: SNI-minted certs from the already-trusted local CA.

**DNS** is already done: the machine's dnsmasq wildcards all of `.test` to
loopback (a fresh machine can use gerry's embedded DNS instead).

**`frontend/vite.config.ts`** — the three lines every Vite-behind-a-proxy
setup needs, each with the reason in a comment: `allowedHosts` (Vite 6
rejects foreign Host headers), `hmr: {protocol: wss, host, clientPort: 443}`
(the HMR socket must come back through the proxy), `strictPort` (drifting to
port+1 silently breaks the route).

**`backend/main.py`** — CORS for exactly `https://coolwebsite.test`. Two
hostnames means the browser applies CORS; the alternative is one hostname
with a Vite `server.proxy` for `/api`, which trades away prod parity.

**`dev.sh`** — two `gerry run` lines. `gerry run --owner X -- CMD` fetches
X's sticky port, sets `$PORT`, substitutes literal `{PORT}` in the args,
and execs your tool with stdio attached — gerry is a port courier, never a
wrapper. The same line works for uvicorn, vite (under bun or node — the
script prefers `bunx --bun vite` when bun is installed), cargo watch,
`go run`, rails. Nothing on the machine can collide with those ports;
nothing in the repo hardcodes them.

## Verified (2026-08-14, live)

Real Chrome session: page renders with a trusted cert, the cross-origin
fetch to the API succeeds (preflight allows exactly the frontend origin),
and an edit to `App.tsx` hot-swapped in the browser through
`wss://coolwebsite.test` in under two seconds.

## Tear down

```sh
pkill -f 'examples/coolwebsite'   # stop the dev servers
gerry down -f gerrymander.yaml    # release the hostnames (ports stay sticky)
```
