# gerrymander

<p align="center">
  <img src="docs/assets/gerry.png" alt="the gerrymander gopher drawing districts" width="520">
</p>

**A hostname and port control plane** — one authority for *who owns which
hostname* in a wildcard zone, and *which process owns which port* on a dev
machine. The same binary, model, and CLI run on a laptop and in production.

> The name: the original 1812 gerrymander was a salamander-shaped district.
> This tool draws the districts fairly.

## The problem

Multi-tenant SaaS on `*.yourdomain.com` has an implicit race: the app's
tenant table and the platform's ingress routes both think they own the
hostname namespace, and neither can see the other. Nothing stops a tenant
claiming `grafana`; nothing stops an engineer shipping a route that shadows
tenant `acme`. Meanwhile on every dev machine, `5173`, `8000` and `8080` are
allocated by convention and collide weekly.

Both are the same operation: **claim a scarce name from a pool, exclusively,
and make traffic arrive.**

## How it works

The entire correctness guarantee is two unique indexes:

- `UNIQUE (zone_id, label)` — tenant claims, platform reservations, and
  blocklist entries all live in **one table**, therefore one namespace. A
  tenant can't take `grafana` because `grafana` is already a row.
- `UNIQUE (pool_id, value)` + `UNIQUE (pool_id, owner_ref)` — ports are
  granted once, and **sticky**: the same owner always gets the same port, so
  it's safe to write into config files.

Around that core:

- **REST API** with availability checks that return *why not* + suggestions,
  two-phase holds (reserve → provision → commit), idempotency keys.
- **Embedded dev proxy**: per-SNI leaf certs from a local CA (bring your own —
  e.g. Caddy's, so existing trust survives), multi-port TLS listeners,
  hot-reloading route table. Replaces a hand-maintained Caddyfile with a
  `gerrymander.yaml` per repo (`gerry up`).
- **Supervised backends**: start a dev server on first request, health-gate,
  sleep it when idle, park it on crash loops. Contained behind a backend
  interface — never a core concern.
- **Kubernetes observer**: watches Traefik `IngressRoute`s and `Ingress`es,
  auto-registers platform hostnames so the reserved list can never drift, and
  reports conflicts — **never auto-resolves them**.
- **Traefik shadow check**: verifies the tenant catch-all's priority stays
  strictly below every bare-`Host()` route. Ties silently hijack hostnames;
  no other tool catches this.
- **MCP server** (`gerry mcp`): coding agents claim ports instead of
  hardcoding 5173 for the fourth time.
- **Prometheus metrics** + Laravel client package (`Rule::hostnameAvailable`,
  fails closed with an embedded blocklist).

## Quickstart (dev machine)

```sh
go install github.com/Nano112/gerrymander/cmd/gerry@latest
# vite projects: bun add -d gerrymander-vite   (or npm i -D)
gerry serve --config gerry.yaml &          # api + proxy + optional DNS
gerry claim --zone myapp.test --label api  # claim a hostname
gerry port --owner myapp/vite              # sticky port, 51000+
gerry up -f gerrymander.yaml               # apply a project manifest
```

Example `gerrymander.yaml`:

```yaml
project: myapp
zone: myapp.test
services:
  app:
    hostnames: [myapp.test, "*.myapp.test"]
    routes:
      - { address: myapp-container:80 }
      - { listen: 5175, address: myapp-container:5175 }
  vite:
    hostnames: [hmr.myapp.test]
    supervised:
      cmd: npm run dev -- --port ${PORT}
      dir: .
      idle_timeout: 30m
```

## Desktop app

<p align="center">
  <img src="docs/assets/desktop-map.png" alt="the district map" width="680">
</p>


`desktop/` is a native app (Wails: Go backend + web UI — macOS now, the same
codebase targets Linux and Windows) for managing the local environment:

<p align="center">
  <img src="docs/assets/desktop-districts.png" alt="Gerrymander desktop — districts view" width="640">
</p>

- **Districts** — the zone tree: every hostname, its routes, owner, and state,
  with claim (live availability check + suggestions) and release.
- **Ports** — every listening TCP socket on the machine (lsof), cross-marked
  with registry grants, with graceful kill (SIGTERM → 2s → SIGKILL; ⌥-click
  for immediate SIGKILL) — the port-killer workflow, registry-aware.
- **Processes** — supervised services: start, stop, live log tail.
- Header controls start/stop the local daemon (docker compose) and point the
  app at any gerry instance (local or remote) in Settings.

```sh
cd desktop && wails build   # produces build/bin/Gerrymander.app
```

## Production (registry mode)

`deploy/k8s/` runs gerry as the hostname authority for a zone: proxy off,
observer on. The app checks availability at signup; the observer imports
everything the cluster actually routes; `/v1/conflicts` and the
`gerry_conflicts` metric surface drift.

## Status

v0.1 — built 2026-08-14, dogfooding on the author's dev machine (replacing a
Caddy dev proxy) and production cluster (registry for a multi-tenant zone).
Race invariants are tested under `-race`; see `docs/superpowers/specs/` for
the full design and `docs/runbook-*.md` for the dogfood setups.

Deferred (designed, not yet built): `dns/cloudflare`, `ingress/caddy`,
`dns/dnsmasq` writers, `HostnameReservation` CRD, admin UI.

## License

MIT
