#!/usr/bin/env bash
# e2e for the actuator safety contract, against a throwaway k3d cluster.
# Proves, for both providers:
#   1. an allocation with a service backend materializes a managed route
#   2. a foreign (unlabeled) route is never touched
#   3. hand-edited drift on a managed route is repaired
#   4. releasing the allocation deletes the managed route (foreign survives)
# Usage: hack/e2e-actuator.sh [traefik-crd|gateway-api|all]
set -euo pipefail

PROVIDER="${1:-all}"
CLUSTER="gerry-e2e-$$"
WORK="$(mktemp -d)"
API_PORT=47901
GERRY_PID=""

cleanup() {
  [ -n "$GERRY_PID" ] && kill "$GERRY_PID" 2>/dev/null || true
  k3d cluster delete "$CLUSTER" >/dev/null 2>&1 || true
  rm -rf "$WORK"
}
trap cleanup EXIT

say() { printf '\n== %s ==\n' "$*"; }
die() { printf 'FAIL: %s\n' "$*" >&2; exit 1; }

say "cluster"
# k3s's bundled Traefik is disabled: this is an object-level test, so we
# install exactly the CRDs we need ourselves — deterministic, no controller.
k3d cluster create "$CLUSTER" --no-lb \
  --k3s-arg '--disable=traefik@server:0' \
  --kubeconfig-update-default=false --kubeconfig-switch-context=false >/dev/null
export KUBECONFIG="$WORK/kubeconfig"
k3d kubeconfig get "$CLUSTER" > "$KUBECONFIG"
API=$(kubectl config view -o jsonpath='{.clusters[0].cluster.server}' | sed 's/0.0.0.0/127.0.0.1/')

wait_crd() {
  kubectl wait --for condition=established "crd/$1" --timeout=120s >/dev/null \
    || die "CRD $1 never became ready"
}
if [ "$PROVIDER" != "gateway-api" ]; then
  kubectl apply -f - >/dev/null <<'YAML'
apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  name: ingressroutes.traefik.io
spec:
  group: traefik.io
  names: { kind: IngressRoute, listKind: IngressRouteList, plural: ingressroutes, singular: ingressroute }
  scope: Namespaced
  versions:
    - name: v1alpha1
      served: true
      storage: true
      schema:
        openAPIV3Schema:
          type: object
          x-kubernetes-preserve-unknown-fields: true
YAML
  wait_crd ingressroutes.traefik.io
fi
if [ "$PROVIDER" != "traefik-crd" ]; then
  kubectl apply -f https://github.com/kubernetes-sigs/gateway-api/releases/download/v1.2.1/standard-install.yaml >/dev/null
  wait_crd httproutes.gateway.networking.k8s.io
fi

kubectl create serviceaccount e2e >/dev/null
kubectl create clusterrolebinding e2e --clusterrole=cluster-admin --serviceaccount=default:e2e >/dev/null
kubectl create token e2e --duration=1h > "$WORK/token"
kubectl config view --raw -o jsonpath='{.clusters[0].cluster.certificate-authority-data}' | base64 -d > "$WORK/ca.crt"
kubectl create deployment shop --image=registry.k8s.io/pause:3.9 >/dev/null
kubectl expose deployment shop --name shop-svc --port 80 >/dev/null

run_provider() {
  local provider="$1"
  say "provider: $provider"

  local extra=""
  if [ "$provider" = "gateway-api" ]; then
    extra='  gateway: { name: edge, namespace: default }'
  fi
  cat > "$WORK/gerry.yaml" <<EOF
db: $WORK/gerry-$provider.db
api: { listen: 127.0.0.1:$API_PORT }
zones: [{ name: e2e.example, profile: prod }]
proxy: { enabled: false }
dns: { enabled: false }
docker_labels: { enabled: false }
observer:
  enabled: false
  api_server: $API
  token_file: $WORK/token
  ca_file: $WORK/ca.crt
actuator:
  enabled: true
  provider: $provider
  zones: [e2e.example]
  interval: 3s
$extra
ports: { ensure_default_pool: false }
EOF
  ./gerry serve --config "$WORK/gerry.yaml" > "$WORK/daemon-$provider.log" 2>&1 &
  GERRY_PID=$!
  sleep 2

  local kind path drift_field
  if [ "$provider" = "traefik-crd" ]; then
    kind="ingressroute"; drift_field='{.spec.routes[0].priority}'
  else
    kind="httproute"; drift_field='{.spec.rules[0].backendRefs[0].port}'
  fi

  # foreign route that must survive everything
  if [ "$provider" = "traefik-crd" ]; then
    kubectl apply -f - >/dev/null <<'YAML'
apiVersion: traefik.io/v1alpha1
kind: IngressRoute
metadata: { name: foreign, namespace: default }
spec:
  entryPoints: [websecure]
  routes:
    - match: Host(`foreign.e2e.example`)
      kind: Rule
      services: [{ name: shop-svc, port: 80 }]
YAML
  else
    kubectl apply -f - >/dev/null <<'YAML'
apiVersion: gateway.networking.k8s.io/v1
kind: HTTPRoute
metadata: { name: foreign, namespace: default }
spec:
  parentRefs: [{ name: edge }]
  hostnames: [foreign.e2e.example]
  rules: [{ backendRefs: [{ name: shop-svc, port: 80 }] }]
YAML
  fi

  # 1. claim → managed route appears
  ALLOC=$(curl -sf -X POST "http://127.0.0.1:$API_PORT/v1/claims" -H 'Content-Type: application/json' -d '{
    "zone":"e2e.example","label":"shop","kind":"tenant","source":"seed","owner_ref":"t1",
    "spec":{"routes":[{"backend":{"kind":"service","service":{"namespace":"default","name":"shop-svc","port":80}}}]}}' \
    | python3 -c 'import json,sys; print(json.load(sys.stdin)["allocation"]["id"])')
  for i in $(seq 1 10); do
    kubectl get "$kind" gerry-e2e-example-shop >/dev/null 2>&1 && break; sleep 2
  done
  kubectl get "$kind" gerry-e2e-example-shop >/dev/null 2>&1 || die "$provider: managed route not created"
  [ "$(kubectl get "$kind" gerry-e2e-example-shop -o jsonpath='{.metadata.labels.app\.gerrymander/managed}')" = "true" ] \
    || die "$provider: managed label missing"
  echo "created ✓"

  # 2+3. drift repair (patch a field, expect it restored)
  if [ "$provider" = "traefik-crd" ]; then
    kubectl patch ingressroute gerry-e2e-example-shop --type=json \
      -p '[{"op":"replace","path":"/spec/routes/0/priority","value":99}]' >/dev/null
    want="10"
  else
    kubectl patch httproute gerry-e2e-example-shop --type=json \
      -p '[{"op":"replace","path":"/spec/rules/0/backendRefs/0/port","value":9999}]' >/dev/null
    want="80"
  fi
  for i in $(seq 1 10); do
    [ "$(kubectl get "$kind" gerry-e2e-example-shop -o jsonpath="$drift_field")" = "$want" ] && break; sleep 2
  done
  [ "$(kubectl get "$kind" gerry-e2e-example-shop -o jsonpath="$drift_field")" = "$want" ] \
    || die "$provider: drift not repaired"
  echo "drift repaired ✓"

  # 4. release → managed gone, foreign intact
  curl -sf -X DELETE "http://127.0.0.1:$API_PORT/v1/allocations/$ALLOC" -o /dev/null
  for i in $(seq 1 10); do
    kubectl get "$kind" gerry-e2e-example-shop >/dev/null 2>&1 || break; sleep 2
  done
  kubectl get "$kind" gerry-e2e-example-shop >/dev/null 2>&1 && die "$provider: managed route survived release"
  kubectl get "$kind" foreign >/dev/null 2>&1 || die "$provider: FOREIGN ROUTE DELETED"
  echo "released, foreign untouched ✓"

  kubectl delete "$kind" foreign >/dev/null
  kill "$GERRY_PID" 2>/dev/null; GERRY_PID=""
  wait 2>/dev/null || true
}

go build -o gerry ./cmd/gerry

case "$PROVIDER" in
  traefik-crd|gateway-api) run_provider "$PROVIDER" ;;
  all) run_provider "traefik-crd"; API_PORT=$((API_PORT+1)); run_provider "gateway-api" ;;
  *) die "unknown provider $PROVIDER" ;;
esac

say "all actuator safety properties hold"
