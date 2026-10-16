# Kubernetes

In production, gerry is the authority on who owns which hostname: the
missing referee between your app's tenant table and your ingress routes.
It layers three capabilities; each is independently optional.

## Install

```bash
helm install gerry deploy/helm/gerrymander \
  --set zones[0].name=yourdomain.com \
  --set monitoring.serviceMonitor=true --set monitoring.prometheusRule=true
kubectl create secret generic gerrymander-api --from-literal=key=$(openssl rand -hex 24)
```

(Or the plain manifests in `deploy/k8s/`.) The API serves on 4780; metrics
on a **separate port** (9091) so an ingress in front of the API can never
expose them. The daemon refuses to start off-loopback with no API key.

## 1. Observer (read-only audit)

Polls Traefik IngressRoutes and Kubernetes Ingresses and compares them with the
registry:

- unregistered hostnames → auto-registered as platform allocations (or
  reported, your choice)
- routes serving a **tenant's** hostname → `kind-mismatch` conflict
- the same hostname served from two namespaces → `duplicate-route`
- bare-Host routes that tie with a tenant catch-all's priority →
  `shadowed-host` (the classic Traefik trap where a tie silently loses
  traffic)

Conflicts appear at `/v1/conflicts`, as `gerry_conflicts` metrics, and as
Prometheus alerts. The observer never mutates Kubernetes resources.

### Retiring disappeared hosts

Temporary routes (for example streaming sessions) otherwise accumulate active
registry records after Kubernetes deletes them. Enable reconciliation explicitly:

```yaml
observer:
  enabled: true
  zones: [yourdomain.com]
  auto_register: true
  retire_missing: true       # default false; requires auto_register
  missing_grace_period: 5m   # default 5m
  interval: 60s
```

Helm exposes these as `observer.retireMissing` and
`observer.missingGracePeriod`. Start with retirement disabled to inspect
`status.observation` and the missing-host metric, then back up the registry
before enabling it for an existing installation. Existing records receive a
fresh absence window; their age is never evidence of disappearance.

Only active `source=observed`, `kind=platform` allocations without routing
backends or reservation expiry qualify. API claims, seed reservations and
tenant allocations are excluded. A host must be absent from **both complete,
paginated lists**, across at least three successful observations and the full
grace period. Failed requests, malformed lists, expired/repeated pagination
tokens, changed resource versions, restarts and gaps over two polling intervals
reset the absence window. No retirement occurs from partial results.

Identity uses zone plus label, including wildcard labels and nested zones,
so an owner replacement or another route serving the same host keeps the
allocation alive. `status.observation` exposes `last_seen_at`, `owner_uid`,
`missing_since` and `missing_observations`. Rediscovery clears absence state.

Retirement atomically writes a `retire-missing` event containing the complete
allocation snapshot, then frees the label for a future claim or rediscovery.
Kubernetes routes are never deleted by this process. The existing `hold_ttl`
continues to apply only to pending reservations. This observer setting does
not enable expiry for local development or API-managed services.

Metrics on the dedicated metrics listener:

- `gerry_observer_last_success_timestamp_seconds` — last complete successful sync
- `gerry_observer_errors_total` — failed cycles
- `gerry_observed_hosts_missing{zone}` — absent records awaiting retirement
- `gerry_observed_hosts_retired_total{zone}` — completed audited retirements

For an enabled observer, alert when the last successful sync is more than a
few polling intervals old. Missing counts can rise briefly during normal
session churn; sustained growth with no retirements warrants investigation.

## 2. Actuator (write-side, off by default)

Give an allocation a `service` backend and the actuator materializes a
route for it:

```yaml
actuator:
  enabled: true
  provider: traefik-crd     # or gateway-api
  zones: [yourdomain.com]
  entry_points: [websecure] # traefik-crd
  # gateway: { name: edge, namespace: infra }   # gateway-api
```

The safety contract, enforced by construction and proven by a k3d e2e in
CI on every change:

1. it lists and mutates **only** resources labeled
   `app.gerrymander/managed=true`
2. hand-edited drift on a managed route is repaired within one interval
3. releasing the allocation deletes the managed route
4. a foreign route is never touched, ever

With `traefik-crd`, route priority is floored above tenant catch-alls, so
the shadow trap is unrepresentable. With `gateway-api`, hostname precedence
is by spec and wildcards use the native `*.` form.

## 3. GitOps input (HostnameReservation CRD)

```yaml
apiVersion: gerrymander.dev/v1alpha1
kind: HostnameReservation
metadata: { name: grafana, namespace: infra }
spec:
  zone: yourdomain.com
  label: grafana
  kind: platform
```

Enable `crd_ingest` and CRs feed the registry. The CR is input; the
database is truth. A reservation that loses the race for a taken label
logs the conflict and stays unfulfilled; it never steals the name. Deleting
the CR releases the allocation (only CRD-owned ones).

## Wiring your app

Your app claims tenant hostnames at signup through the API (a Laravel
client ships in `clients/laravel`; the API is four endpoints for any other
stack). Use [scoped tokens](tokens.md) so the app's credential can only
touch tenant claims. The result is a closed loop: signup → claim (reserved
names enforced) → actuator creates the route → observer stays quiet because
it recognizes gerry's own work → Prometheus alerts on anything that drifts.
