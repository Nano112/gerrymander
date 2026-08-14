# Multiple machines, other TLDs

## Any TLD you like

`.test` is a default, not a requirement. Zones are just names:

```sh
gerry zone add --name myapp.internal --profile dev
export GERRY_TLD=internal    # gerry init now scaffolds <project>.internal
```

`gerry setup` derives resolver wiring from the zones the daemon actually
has, whatever their TLD. Three TLDs deserve caution:

| TLD | Why |
|---|---|
| `.dev`, `.app` | Real, Google-owned, **HSTS-preloaded**: browsers force HTTPS and refuse untrusted certs with no bypass. Workable only after `gerry trust`. |
| `.local` | Owned by mDNS/Bonjour; resolvers treat it specially. Avoid. |
| `.localhost` | Browsers hardwire it to loopback — fine locally, useless on a tailnet. |

`.test` is IETF-reserved for exactly this purpose, which is why it is the
default; `.internal` is the other officially blessed choice.

## Several machines running gerry

Each daemon is its own registry. The one rule that keeps this sane:
**one zone, one authority.** Never register the same zone on two daemons —
two registries answering for one namespace is precisely the race gerry
exists to remove.

Three patterns that follow from it:

### 1. One authority, many clients

One machine runs the daemon; the others talk to it:

```sh
# on a second machine (no daemon needed)
export GERRY_API=http://100.x.y.z:4780   # the authority, over the tailnet
gerry ls
gerry claim --zone myapp.test --label api
GERRY_API=http://100.x.y.z:4780 gerry trust   # once, for its CA
```

Backends still need to be reachable from the proxy machine (tailnet IPs
work as backend addresses).

### 2. A zone per machine

Each machine owns its own namespace and runs the full stack:

```sh
# laptop:   zones: [mac.test]
# desktop:  zones: [desk.test]
```

Tailnet split DNS routes are **per domain**, so each zone can point at
its machine: `mac.test → 100.x.y.1`, `desk.test → 100.x.y.2`. Every
device then resolves `app.mac.test` to the laptop and `app.desk.test` to
the desktop. `gerry tailnet` on each machine walks through its own row.

### 3. Same project, moving between machines

Keep the manifest in the repo (it already is: `gerrymander.yaml`) and
let whichever machine you're on claim it in that machine's zone — or use
pattern 1 so the claim lives in one registry regardless of where you
type. Sticky ports are per-registry: pattern 1 gives you the same port
everywhere, pattern 2 gives each machine its own.
