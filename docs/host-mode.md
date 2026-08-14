# Host mode

Run the gerry daemon directly on the machine instead of in docker. This is
the recommended end-state for local dev: **supervised backends work** (the
proxy and your dev processes share a host, so gerry boots them on first
request and sleeps them when idle), file-watching never crosses a VM
boundary, and one hop of latency disappears.

```sh
gerry service install     # launchd user agent + starter config (~/.gerrymander/gerry.yaml)
gerry service status
gerry service restart
gerry service uninstall
```

Validated lifecycle (2026-08-14, uvicorn behind a host proxy):

| step | observed |
|---|---|
| cold start | first request held 0.87s while the process booted + health-gated, then 200 |
| warm | 4ms |
| idle | stopped after the manifest's `idle_timeout` |
| re-wake | 448ms on the next request, same sticky port |

## Migrating a machine from the container daemon

The container and host daemons want the same ports (80/443/517x/4780), so
this is a swap, not a coexistence:

1. **Backends must be reachable from the host.** Container-mode manifests
   reach docker-network aliases (`olsyn-app:80`); host mode cannot. Every
   such backend needs a published port and a manifest edit to
   `127.0.0.1:<published>` — or a move to `supervised:`. Do this first.
2. Copy the CA so browser trust survives:
   `cp deploy/dev/data/ca/* ~/.gerrymander/ca/`
3. Copy or re-seed the registry: either move the SQLite file from the
   container volume to `~/.gerrymander/gerry.db`, or re-run `gerry up` in
   each project (sticky ports live in the DB — copy it to keep them).
4. Swap: `docker compose down` (deploy/dev) → `gerry service install`.
5. `gerry status` should go all-green; every hostname serves as before.

Rollback is the reverse: `gerry service uninstall` →
`docker compose up -d` in deploy/dev.
