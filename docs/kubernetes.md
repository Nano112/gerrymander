# Kubernetes

In production, gerry is the **authority on who owns which hostname** — the
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

Polls IngressRoutes / Ingresses / HTTPRoutes and compares them with the
registry:

- unregistered hostnames → auto-registered as platform allocations (or
  reported, your choice)
- routes serving a **tenant's** hostname → `kind-mismatch` conflict
- the same hostname served from two namespaces → `duplicate-route`
- bare-Host routes that tie with a tenant catch-all's priority →
  `shadowed-host` (the classic Traefik trap where a tie silently loses
  traffic)

Conflicts appear at `/v1/conflicts`, as `gerry_conflicts` metrics, and as
Prometheus alerts. **The observer never mutates anything it observes.**

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

The safety contract — enforced by construction and proven by a k3d e2e in
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

Enable `crd_ingest` and CRs feed the registry. **The CR is input; the
database is truth**: a reservation that loses the race for a taken label
logs the conflict and stays unfulfilled — it never steals the name. Deleting
the CR releases the allocation (only CRD-owned ones).

## Wiring your app

Your app claims tenant hostnames at signup through the API (a Laravel
client ships in `clients/laravel`; the API is four endpoints for any other
stack). Use [scoped tokens](tokens.md) so the app's credential can only
touch tenant claims. The result is a closed loop: signup → claim (reserved
names enforced) → actuator creates the route → observer stays quiet because
it recognizes gerry's own work → Prometheus alerts on anything that drifts.
