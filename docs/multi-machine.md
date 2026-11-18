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

## Open a remote service in Rex on macOS

The experimental Rex integration lets each viewer choose a local suffix for a
remote host. For example, starting a service on the server prints
`https://drive.example.test/`. Command-clicking the link opens
`https://drive.example.dev/` on the viewer's Mac. One loopback Rex forward
carries requests for the mapped zone to the remote Gerry HTTPS proxy. Gerry
uses the original hostname for upstream TLS and HTTP routing.

### One-time setup

1. Use a Rex client and server with TCP tunnel support. This flow was tested
   with Rex 0.1.0-preview.21 on both ends. The viewer needs the Gerry daemon
   and macOS desktop app from this branch. Open the desktop app once to
   register its `gerry://` handler.
2. In the remote shell environment, set `GERRY_REX_HOST=mini`. This is the
   lookup key that each viewer configures. In an interactive Rex session,
   `gerry dev` and the updated Gerry Vite plugin print clickable service URLs.
3. On the viewer, create `~/.gerrymander/rex-hosts.json`:

   ```json
   {
     "mini": {
       "server": "mac-mini",
       "session": "work",
       "remote_zone": "example.test",
       "local_zone": "example.dev",
       "proxy_port": 443,
       "ca_cert": "/Users/you/.gerrymander/mini-root.crt",
       "rex": "/Applications/Rex Beta.app/Contents/Helpers/Rex Server.rex/Contents/MacOS/rex"
     }
   }
   ```

   Use the server label from the viewer's Rex host list and an existing remote
   session for the tunnel. A session can be omitted when Rex can select one
   unambiguously. Set `proxy_port` to the remote Gerry HTTPS listener, which
   may be 8444 on a machine that shares port 443 with another proxy. Copy the
   remote Gerry **public root certificate** to `ca_cert`. The viewer's browser
   uses the viewer's usual Gerry CA. The optional `rex` path selects the binary
   when the daemon's PATH does not contain the desired Rex version.
4. Configure the viewer's DNS to resolve the specific local zone
   (`example.dev` and its subdomains) to loopback. With Gerry's embedded DNS
   on macOS, include that zone in `dns.zones` and route the zone to its local
   listener with a matching `/etc/resolver/example.dev` entry. Enable the
   viewer's Gerry HTTPS proxy on loopback port 443 and restart its daemon to
   load the mapping.

### Daily use

Run the usual dev script on the remote machine and Command-click the **Gerry**
URL in its output. Rex may ask before opening a custom-scheme link. The first
local HTTPS request starts a forward. Subsequent requests, including WebSocket
upgrades, reuse it. Existing local Gerry routes take precedence. Set app-specific
absolute URLs, cookie domains and redirects for the local alias where needed.
The Vite plugin uses the browser's hostname for HMR in Rex mode. Applications
with their own Vite configuration should also use the browser's hostname.

To change the mapping, edit the viewer's host file and restart its daemon.
Removing the entry and restarting closes the managed forward. For isolated
setups, both the daemon and desktop app support an alternate config path via
`GERRY_REX_HOSTS`.
