# Changelog

All notable changes to gerrymander. Versions follow [semver](https://semver.org);
until 1.0, minor bumps may include breaking changes (called out explicitly).

## v0.5.0 — 2026-08-14

### Added
- **Kubernetes actuator** (`actuator:` config, off by default): materializes
  routes for allocations that carry `service` backends. Two providers:
  `traefik-crd` (IngressRoutes, priority floor above tenant catch-alls) and
  `gateway-api` (HTTPRoutes attached to a configured Gateway, native `*.`
  wildcards). Both only ever create/update/delete resources labeled
  `app.gerrymander/managed=true`, repair drift, and remove routes when
  allocations are released — proven by a k3d e2e that runs in CI.
- **Scoped API tokens** (`gerry token create|ls|revoke`): owner-scoped
  credentials confined to one owner's tenant hostnames — claims forced to
  the token's identity, listings filtered, every admin surface closed.
  Plaintext shown once, SHA-256 stored, revocation immediate. The
  separation needed to hand registry access to tenants or CI jobs.
- **Audit trail endpoint**: `GET /v1/allocations/{id}/events` exposes the
  append-only event history, under the same ownership rules.
- **Compose-label auto-claim**: label a container
  `gerrymander.hostname=api.myapp.test` (optional `gerrymander.port`,
  `gerrymander.network`) and the hostname exists for the container's life —
  `docker compose up` claims, `down` releases. Only touches allocations it
  created (`owner_kind=docker-label`).
- **HostnameReservation CRD** (`gerrymander.dev/v1alpha1`) + `crd_ingest`:
  declare registry entries in Git; the CR is input, the database is truth —
  reservations that lose a race stay unfulfilled instead of stealing names.
- **Helm chart** (`deploy/helm/gerrymander`): registry + observer, opt-in
  actuator (RBAC widens automatically), monitoring toggles.
- **Cloudflare DNS sync** (experimental): per-label records for active
  allocations; only records commented `gerrymander-managed` are ever
  touched.
- **Windows builds (beta)**: cross-compiled zips in releases; `gerry dev`
  and supervision use `cmd /C` + `taskkill /T`; `gerry trust` via certutil;
  `gerry setup` prints the NRPT recipe. Not yet exercised on real Windows.
- **Versioned schema migrations** (`PRAGMA user_version` on SQLite, a
  `schema_migrations` table on Postgres). Databases created by earlier
  versions adopt the baseline transparently; future upgrades are ordered
  and recorded.
- `gerry_http_requests_total` / `gerry_http_request_seconds` metrics,
  labeled by matched route pattern (bounded cardinality).
- `api.metrics_listen`: serve `/metrics` on a dedicated listener and remove
  it from the API mux, so an ingress in front of the API can't expose
  registry metrics.
- ServiceMonitor + PrometheusRule examples (`deploy/k8s/monitoring.example.yaml`):
  hostname-conflict and unregistered-host warnings, registry-down critical.
- `gerry setup` / `gerry uninstall`: marker-tagged resolver files and CA
  material; uninstall only removes what gerry itself created (dry-run by
  default) and can never break DNS it didn't set up.
- `gerry dev` procfile mode: run every service with a `dev:` command,
  prefixed output, group shutdown.
- `gerry trust`, `@local` backend sentinel (host/container portability),
  synchronous route-table refresh on API mutations, positional
  `gerry rename <fqdn> <label>`, `gerry zone rm`.

### Fixed
- The observer no longer classifies the actuator's own managed routes as
  conflicts (kind-mismatch/duplicate/auto-register are skipped for routes
  gerry itself wrote).
- The API refuses to start with a non-loopback listen and no API key
  (`api.allow_unauthenticated` opts out deliberately). Previously auth
  silently disabled itself when the key env was empty.
- Claims default to `kind=platform` in dev-profile zones (the CLI no longer
  hardcodes `tenant`, which tripped the reserved-name blocklist on labels
  like `api`).
- Busy proxy ports degrade the proxy instead of killing the daemon, and the
  holder is named in the error.

### Breaking
- None known. Existing SQLite databases are adopted at schema version 1 on
  first open.

## v0.3.1 — 2026-08-14

Linux-clean CLI: systemd --user services, platform-aware doctor, NO_COLOR
and TTY-aware output, `gerry completion bash|zsh|fish`, release binaries
for darwin (cgo) and linux (static).

## v0.3.0 — 2026-08-14

First public release: hostname registry (zones/allocations/ports), local
dev mode (TLS proxy + DNS + CA + supervision + docker relays), manifest
apply with pruning, `gerrymander-vite` plugin, Kubernetes observer with
conflict detection and the catch-all shadow check, desktop app (macOS),
MCP server.
