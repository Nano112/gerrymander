# Coexistence — what gerry touches, what it never touches

gerry's rule for living on your machine: **every takeover is detected
before it happens, and every removal requires gerry's own ownership mark.**
`gerry uninstall` is dry-run by default and can list its entire footprint.

## The footprint (complete inventory)

| Thing | Created by | Marked how | Removed by uninstall? |
|---|---|---|---|
| launchd plist / systemd user unit | `gerry service install` | fixed name (`com.gerrymander.daemon` / `gerrymander.service`) | yes |
| `/etc/resolver/<tld>` (macOS) | `gerry setup` | first line `# gerrymander-managed` | **only with the marker** |
| System-trust CA | `gerry trust` | CN `gerrymander local CA` | **only that CN** |
| docker relay containers | docker backends | label `app.gerrymander.relay` | yes (by label) |
| `~/.gerrymander/` (db, CA, config, logs) | daemon | path | only with `--purge` |
| the `gerry` binary | you | — | never (told where it is) |

**Why uninstall cannot break DNS:** the only DNS artifact gerry ever
creates is a marker-tagged per-TLD resolver file. Removing it restores the
exact pre-gerry path — your upstream resolver answers, and dev TLDs simply
NXDOMAIN. Resolver files without the marker (dnsmasq's, Herd's, yours) are
reported and left alone, always. Same logic for TLS: a CA gerry merely
*borrowed* (say, Caddy's already-trusted root) has a different CN and is
never deleted.

## Interference matrix — and what gerry does about each

**Ports 80/443 taken** (Herd, Valet, Caddy, nginx, another proxy, a docker
container): the proxy **degrades instead of dying** — the registry API
stays up, the log and `gerry status` name the process holding the port
(via lsof) with the choice: stop it or move `proxy.tls` in the config.

**Port 4780 taken**: if the holder answers like a gerry daemon, the error
says so explicitly ("container mode running? stop it first") — the
two-daemons foot-gun is caught at startup, not debugged at midnight.

**Existing dev-DNS (dnsmasq, Herd, Valet)**: `gerry setup` probes
resolution first and *skips* any TLD that already resolves, naming the
likely owner. gerry never edits another tool's files.

**systemd-resolved / VPN DNS (Tailscale MagicDNS etc.)**: macOS
per-TLD resolver files coexist with VPN DNS. On Linux gerry prints manual
routing instructions rather than guessing at resolved's config.

**Intercepting proxies (Proxyman, Charles, corporate)**: `gerry status`
detects an active system/env HTTP proxy and warns — the signature is
"works in curl, dies in the browser", and the stale-socket variant
survives daemon restarts (learned the hard way).

**mDNSResponder on 5353**: gerry's embedded DNS binds unicast
`127.0.0.1:5353`, which coexists with mDNS multicast; resolver files carry
an explicit `port 5353` line.

**OS ephemeral port range** (macOS hands out 49152–65535): sticky grants
are bind-tested before issue and marked `occupied-foreign` when something
unmanaged squats later — reported, never silently reassigned.

**Preloaded-HSTS TLDs**: `.dev`, `.app` and friends are real, HSTS-preloaded
gTLDs; browsers refuse local CAs for them. Use `.test` (RFC 6761 reserved).

**Firefox**: keeps its own trust store; import the CA there manually or
enable `security.enterprise_roots.enabled`.

**Docker absent/stopped**: docker backends fail with a clear error and
everything else keeps working; relays restart themselves (`--restart
unless-stopped`) when docker returns.
