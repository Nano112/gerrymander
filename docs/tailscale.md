# Tailscale

Your dev hostnames can resolve from every machine on your tailnet: the
laptop, the work machine, your phone. One config line makes gerry's DNS
answer with this machine's tailnet address instead of loopback; split DNS
on the tailnet routes the dev TLD here.

## On the machine running gerry

```yaml
dns:
  enabled: true
  listen: ":53"          # must be reachable from peers, not loopback
  zones: [test]
  advertise: tailscale   # answers carry this machine's tailnet IPv4
```

`advertise` also accepts a literal IP. With an IPv4 advertised, AAAA
queries return empty on purpose: handing a peer `::1` for a remote machine
would break resolution.

The proxy already listens on all interfaces (`proxy.tls: ":443"`), so
peers reach it as soon as they can resolve it.

## On the tailnet (split DNS)

Point the dev TLD at this machine's tailnet IP.

**Tailscale admin console**: DNS → Split DNS → domain `test`, nameserver
`100.x.y.z` (this machine).

**Headscale**: in the config's `dns` section:

```yaml
dns:
  split:
    test: ["100.x.y.z"]
```

## Trust on the peers

TLS still comes from gerry's local CA, so each peer that should get a
green padlock needs it once:

```sh
GERRY_API=http://100.x.y.z:4780 gerry trust
```

(`/v1/ca` is unauthenticated by design; the CA certificate is public
material.) Phones: fetch `http://100.x.y.z:4780/v1/ca` in the browser and
install the profile, or skip trust and accept the warning.

## What this is good for

- Opening `https://myapp.test` from a phone on the tailnet to test mobile
  layouts against your dev server.
- A second dev machine using the first one's hostnames and registry
  (`GERRY_API=http://100.x.y.z:4780 gerry ls`).
- Demoing to a colleague on the tailnet without deploying anything.

Port 53 needs root or a capability on most systems; `gerry service
install` runs the daemon as your user, so either grant the binary
`CAP_NET_BIND_SERVICE` (Linux), keep DNS on a high port with a forwarder
in front, or run split DNS against a per-zone high-port setup if your
tailnet DNS supports it (Tailscale's does not; headscale forwards to :53).
