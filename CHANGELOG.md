# Changelog

All notable changes to gerrymander. Versions follow [semver](https://semver.org);
until 1.0, minor bumps may include breaking changes (called out explicitly).

## v0.7.1 — 2026-08-15

- `gerry update` (brew path) pulls the tap before upgrading, fixing
  "already installed" when updating right after a release.

## v0.7.0 — 2026-08-15

- `gerry tailnet`: guided, self-verifying setup for tailnet-wide dev
  hostnames — probes machine-subdomain resolution, split DNS, and
  machine-name ports against the real tailnet, and walks through exact
  fixes for whatever is missing.

## v0.6.2 — 2026-08-15

- `gerry update [--check]`: self-update from the latest release (atomic
  replace; Homebrew installs defer to brew).
- bootstrap surfaces the tailnet integration when tailscale is present.
- gerrymander-vite allows machine-name subdomains for tailnets with the
  dns-subdomain-resolve node capability.

## v0.6.1 — 2026-08-15

- gerry status detects the two tailnet traps (TLS-terminating serve handler
  on :443; advertised zone without a split-DNS route) with exact fixes.
- GET /v1/dns exposes the DNS config; gerrymander-vite 0.2.0 auto-allows
  the machine MagicDNS name.

## v0.6.0 — 2026-08-15

### Added
- **Tailscale**: `dns.advertise: tailscale` answers dev-zone queries with
  this machine's tailnet IP — split DNS on the tailnet makes your dev
  hostnames resolve from every device (headscale recipe in the docs).
- **nginx writer** (`nginx_sync`): one marker-tagged include file rendered
  from the registry + reload; files without the marker are never
  overwritten.
- **Nginx Proxy Manager writer** (`npm_sync`): proxy hosts via NPM's REST
  API, marker in advanced_config, partial updates so UI-attached
  certificates survive, UI-made hosts invisible.
- **MCP**: `rename_hostname` and `registry_status` tools (11 total).
- **Agent skill** (`skills/gerry`): teaches Claude Code to allocate ports
  and hostnames through the registry instead of guessing.

## v0.5.2 — 2026-08-15

- `gerry init` detects the project's dev command: a package.json with a
  `dev` script scaffolds a runnable `dev:` line, runner picked from the
  lockfile present (bun/pnpm/yarn/npm). `gerry init && gerry dev` works
  without hand-editing the manifest.

## v0.5.1 — 2026-08-14

- `gerry bootstrap`: the whole first run as one idempotent command — service
  install (skipped when a daemon already serves the API, e.g. container
  mode), wait for health, then DNS + trust. The curl installer runs it
  automatically when a tty is available (`GERRY_INSTALL_ONLY=1` opts out).

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
