---
name: gerry
description: Use when starting any dev server, choosing a port, creating a local hostname, or debugging port conflicts / "address already in use" — the machine runs a gerrymander registry, so allocate names and ports through it instead of guessing. Also use when asked to set up a project's dev environment or expose a container locally.
---

# gerry: allocate, don't guess

This machine runs gerrymander, an authority for local hostnames and dev
ports. Ports 5173/8000/8080 are contended; hostnames like `myapp.test` are
registered. Guessing either causes the collisions you were asked to avoid.

## The rules

1. **Never hardcode a port.** Ask the registry for one; the same owner
   always receives the same port, so it is safe to write into config:

   ```sh
   gerry port --owner <project>/<service> -q     # prints just the number
   gerry run --owner myproj/api -- uvicorn main:app --port '{PORT}'
   ```

2. **Never invent a hostname.** Check, then claim:

   ```sh
   gerry avail --zone myproj.test --label api
   gerry claim --zone myproj.test --label api --owner myproj/api
   ```

   A rejected claim returns the reason and suggestions; use one instead of
   retrying variants blindly.

3. **Setting up a project's dev environment**: `gerry init` scaffolds
   `gerrymander.yaml` (it detects `package.json` dev scripts), and
   `gerry dev` runs everything with ports granted and hostnames routed.
   Prefer that over hand-rolling `npm run dev -- --port 3001`-style
   workarounds.

4. **Port conflict debugging**: `gerry status` names what holds a port and
   whether the daemon, DNS and TLS trust are healthy. Read it before
   killing processes.

5. **Containers**: label instead of publishing ports —
   `gerrymander.hostname=api.myproj.test` on a container gives it a routed
   hostname for its lifetime. `docker compose down` releases it.

6. Release what you created when the task ends: `gerry down` for a
   manifest, `gerry release --id N` for one-off claims. Leave no orphans.

## MCP

When the `gerry` MCP server is connected, the same operations exist as
tools: `claim_port`, `claim_hostname`, `check_availability`,
`list_claims`, `rename_hostname`, `release`, `registry_status`,
`start_service` / `stop_service` / `tail_logs` for supervised processes.
Call `registry_status` first to see the machine's actual state.

## What NOT to do

- Don't edit `/etc/hosts`; DNS for dev zones is already handled.
- Don't `kill -9` a listener just because a port you wanted is busy; that
  listener may be a supervised service another project depends on.
- Don't bypass a claim rejection by picking `api2`, `api-new`, `apix` in a
  loop — the rejection carries suggestions for a reason.
