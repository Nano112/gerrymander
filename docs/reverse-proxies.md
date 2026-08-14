# nginx and Nginx Proxy Manager

gerry's embedded proxy is one dataplane, not the only one. If a machine
already runs nginx (or Nginx Proxy Manager), the registry can drive it:
allocations stay the source of truth, your proxy keeps serving.

Both writers follow the safety contract used everywhere else in gerry:
they mark what they create and only ever touch marked things.

## Plain nginx: `nginx_sync`

Renders active allocations with `address` backends into one include file
and reloads:

```yaml
nginx_sync:
  enabled: true
  conf_path: /opt/homebrew/etc/nginx/servers/gerry.conf
  listen: "80"                # or "443 ssl" with your own cert directives
  reload_cmd: nginx -s reload # "" = write only
```

The file starts with a marker line; a pre-existing file without the marker
is refused, never overwritten. Server blocks carry websocket upgrade
headers and `X-Forwarded-*`; the `@local` backend sentinel becomes
`127.0.0.1`. Everything outside that one file (your `nginx.conf`, other
server blocks, certificates) is invisible to gerry.

## Nginx Proxy Manager: `npm_sync`

NPM keeps its nginx internal, so this writer speaks NPM's REST API
instead: allocations become proxy hosts, visible and editable in the NPM
UI alongside everything you made by hand.

```yaml
npm_sync:
  enabled: true
  url: http://100.71.144.24:81       # the NPM admin API
  # identity_env: NPM_IDENTITY        (defaults shown)
  # secret_env:   NPM_SECRET
  local_host: host.docker.internal    # what "@local" forwards to
  zones: [myzone.test]
```

Each created proxy host carries `# gerrymander-managed` in its advanced
config; hosts made in the UI never match and are never touched. Releasing
an allocation deletes its proxy host; hand-edited drift on a managed host
is repaired on the next pass. Certificates and access lists remain NPM's
job — attach them in the UI as usual (gerry does not overwrite fields it
does not manage on update... it manages domain names, forwarding target
and the marker).

Pair either writer with [Tailscale DNS advertising](tailscale.md) and a
zone resolves tailnet-wide while nginx on one machine serves it.
