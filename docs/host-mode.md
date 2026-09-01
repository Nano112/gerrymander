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

## Reaching the registry from inside a container

A host-mode daemon binds `127.0.0.1:4780`, which no container can reach.
Widening `api.listen` to `0.0.0.0` fixes that and publishes your registry to
every machine on the network at the same time.

`extra_listen` adds listeners without touching that decision. The `@docker`
sentinel expands to the host's docker bridge gateway addresses — the
addresses containers already resolve `host.docker.internal` to, and the only
ones they can use. Docker bridge subnets are not routed off the host, so the
result stays as private as loopback:

```yaml
api:
  listen: 127.0.0.1:4780
  extra_listen: ["@docker"]        # or "@docker:4781" to use another port
```

Then, from inside any container:

```dotenv
GERRY_API=http://host.docker.internal:4780
```

Notes:

- Bare `@docker` reuses `api.listen`'s port, so the registry answers on one
  number everywhere.
- Gateways are resolved at startup. Creating a docker network afterwards
  does not add a listener — restart the daemon (`gerry service restart`).
- Extra listeners never block startup: if docker is absent or a network was
  pruned, gerry logs and serves on `api.listen` alone.
- A literal address in `extra_listen` (not the sentinel) is held to the same
  rule as `api.listen`: off-host and keyless is refused.

## Migrating a machine from the container daemon

The container and host daemons want the same ports (80/443/517x/4780), so
this is a swap, not a coexistence:

1. **Convert docker-alias backends.** Container-mode manifests reach
   docker-network aliases (`address: my-app:80`); a host daemon cannot dial
   those directly. Change each to the `docker:` backend form,
   `docker: { network: <net>, host: my-app, port: 80 }`, and gerry
   maintains a socat relay on that network automatically (no compose
   edits, no published ports). Host processes use `address:`/`supervised:`
   as usual.
2. Copy the CA so browser trust survives:
   `cp deploy/dev/data/ca/* ~/.gerrymander/ca/`
3. Copy or re-seed the registry: either move the SQLite file from the
   container volume to `~/.gerrymander/gerry.db`, or re-run `gerry up` in
   each project (sticky ports live in the DB; copy it to keep them).
4. Swap: `docker compose down` (deploy/dev) → `gerry service install`.
5. `gerry status` should go all-green; every hostname serves as before.

Rollback is the reverse: `gerry service uninstall` →
`docker compose up -d` in deploy/dev.
