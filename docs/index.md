# gerrymander

<p align="center">
  <img src="assets/gerry.png" alt="the gerrymander gopher drawing districts" width="480">
</p>

**A hostname and port control plane.** One authority for *who owns which
hostname* in a wildcard zone, and *which process owns which port* on a dev
machine. The same binary, model, and CLI run on a laptop and in production.

![gerry init → gerry dev demo](assets/demo.gif)

## Sixty seconds to a real hostname

```bash
curl -fsSL https://raw.githubusercontent.com/Nano112/gerrymander/main/install.sh | sh
```

That's the whole setup: it detects your platform, installs the binary, puts
the daemon on login, wires DNS for dev zones, and installs the TLS trust —
all reversible and marker-tagged. (Prefer brew? `brew install
nano112/tap/gerry && gerry bootstrap` is the same thing.) Then:

```bash
cd my-app && gerry init            # scaffolds gerrymander.yaml
bun run dev                        # → https://my-app.test, trusted TLS, done
```

`gerry uninstall` removes exactly what `setup` created and can never break
DNS it didn't set up.

## What it solves

Multi-tenant SaaS on `*.yourdomain.com` has an implicit race: the app's
tenant table and the platform's ingress both think they own the hostname
namespace, and neither can see the other. Nothing stops a tenant claiming
`grafana`; nothing stops an engineer shadowing tenant `acme` with a new
route. Meanwhile on every dev machine, `5173` and `8000` are allocated by
convention and collide weekly.

Both are the same operation: **claim a scarce name from a pool, exclusively,
and make traffic arrive.** gerrymander is that operation as a service:

- **Registry** — `UNIQUE(zone, label)` over tenant claims, platform
  reservations, and a reserved-word blocklist, in one namespace. Sticky port
  pools with the same semantics.
- **Dev mode** — TLS proxy with its own CA, embedded DNS, process
  supervision, docker relays, a Vite plugin, and compose-label auto-claim.
  `bun run dev` is the whole interface.
- **Kubernetes** — an observer that audits what your ingress actually
  serves against the registry (with Prometheus alerts), and an actuator
  that materializes routes (Traefik or Gateway API) from allocations —
  only ever touching resources it labeled, proven by CI e2e.
- **Scoped tokens** — hand a tenant or CI job a credential that can manage
  its own hostnames and nothing else.
- **MCP** — your AI agent asks the registry for a port and a hostname
  instead of guessing 8000. See [AI agents](agents.md).

## Where next

- [Quickstart](quickstart.md) — local dev, end to end
- [Frameworks](frameworks.md) — Vite, Python, docker, anything
- [Kubernetes](kubernetes.md) — observer, actuator, Helm, GitOps CRD
- [Scoped tokens](tokens.md) — multi-tenant credentials + audit trail
