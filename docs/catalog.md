# Application and service catalog

Gerry's catalog is the shared identity library for developer tools, infrastructure
views and application launchers. It works in the same local SQLite daemon as
hostname allocation; it needs no AWS account or cloud registry. A deployed Gerry
instance exposes the same API with authenticated readers.

## Contract

| Entity | Purpose | Identity |
|---|---|---|
| Application | Project name, description, logo, icon, color and links | Stable `id`; editable display name |
| Service | An application component and its role | Stable `id` and manifest key |
| Deployment | One service in an environment and optional workspace | Stable id, explicit environment, workspace and zone |
| Resource | A declared database, bucket, server or other dependency | Stable id, kind, provider/native id, related service ids |
| Capabilities | Permission labels, entry permission and role definitions | Immutable `(service_id, version)` |
| Asset | A logo available independently of the source checkout | Immutable SHA-256 digest |

The public Go package `github.com/Nano112/gerrymander/catalog` contains the typed
schema and client. JSON responses declare `schema_version: 1`. Routes and ports
remain separate existing APIs. The catalog never extracts supervised commands,
environment variables, credentials, user grants or tenant allocations.

Resources are **declared inventory**, not discovered or verified infrastructure.
An AWS ARN or other provider id can correlate a declaration with future discovery.
Neither `registered` nor `inactive` is a health measurement. Monitoring systems
continue to own observations, alerts, packet data and time-series metrics.

## Declare and publish

See [the runnable example](../examples/catalog/gerrymander.yaml). Existing routing
manifests need no new fields: applying them also registers their app, services and
development deployments. Explicit ids are recommended before external consumers
start storing references. Keep them unchanged when renaming display labels.

```yaml
project: atlas
application:
  id: app:atlas
  display:
    name: Atlas
    logo: ./logo.svg
    icon: application
    color: '#7259DB'
    links:
      docs: https://example.com/docs
environment: development
workspace: alice
zone: atlas-alice.test
services:
  web:
    id: atlas-web
    role: web
    display:
      name: Atlas console
    hostnames: [atlas-alice.test]
    port_pool: dev
    capabilities:
      version: v1
      entry_permission: access
      permissions:
        access: Open console
        jobs.read: Read jobs
      roles:
        viewer: [access, jobs.read]
    deployment:
      capability_version: v1
resources:
  database:
    kind: database
    provider: local
    display:
      name: Development database
      icon: postgres
    services: [web]
```

```sh
gerry up -f examples/catalog/gerrymander.yaml
gerry catalog --application app:atlas
gerry catalog --access --environment development --workspace alice
gerry down -f examples/catalog/gerrymander.yaml
```

`gerry up`, `gerry dev` and the updated Vite plugin import relative logo files
before applying the manifest. Files must stay inside the manifest directory,
including symlink targets. PNG, JPEG and static SVG are supported up to 512 KiB;
SVG scripts, CSS, embedded documents and external references are rejected.
API publishers upload an asset first and use its `sha256:…` reference; the daemon
never reads a client-provided file path. Direct API users must send prepared YAML.

For an existing remotely hosted application use `catalog_only: true` on the
service and `deployment.launch_url: https://example.com`. Such a service declares
no routing backend, hostname or dev command. Catalog-only/resource-only manifests
need no zone and do not create or prune routes.

Default ids derive from project/service keys and deployment scope. Supplying
`application.id`, service `id`, `deployment.id` or resource `id` overrides those
defaults. Existing service-access keys can be retained as service ids. Service
keys, parent identities and deployment environment/workspace/zone/owner are
immutable; use a new deployment id when moving between scopes. Display data and
deployment endpoints can change. Service display fields inherit from their
application in access responses; generic consumers can call `InheritDisplay`.

## Local workspaces and lifecycle

The default environment is `development`; set `production` explicitly in a
production manifest. Workspace is optional and defaults to an empty shared
workspace. Consumers must select both environment and workspace, rather than
guessing from hostnames. A read token can inspect the whole registry: these
selectors are filtering, not tenant authorization boundaries.

Without a workspace, legacy routing ownership remains `project/service`.
Opting into a workspace uses `project@workspace/service` and separate sticky ports.
Use distinct dev zones/hostnames for different workspaces: declaring a workspace
does not let it take another workspace's hostname. Use separate local and
production daemons; changing an environment label is not a routing sandbox.

`gerry up` retains its additive routing behavior; `--prune` releases routes and
deactivates deployments removed from that manifest scope. `gerry dev` and the
Vite plugin prune by default. `gerry down` releases declared routes and marks the
declared deployments inactive. Identities, version history and declared resources
are retained. Omission from a bundle does not delete them. Resource deletion,
provider reconciliation and garbage collection are not yet implemented.

Catalog bundle writes are transactional. Manifest apply preflights catalog
conflicts before route changes, then publishes after routing succeeds. Routing
and catalog publication are separate transactions; an interrupted or conflicting
apply can require a retry. This follows existing route reconciliation semantics.

## Service Access integration

Gerry publishes **definitions**, not authorization decisions. An access system
continues to own accounts, membership, grants, API credentials, expiry and audit.
Store grants against stable service ids and permission keys, never display names.

1. Publish a capability version in `capabilities`.
2. Explicitly select it with `deployment.capability_version`.
3. Read the access catalog for the intended environment/workspace.

Publishing v2 never activates it. A different definition under an existing
version returns HTTP 409. The access endpoint includes only registered deployments
with an explicitly selected, published version; it never selects "latest".
Omitting the selection removes that deployment from access discovery on its next
apply. Review version changes before activating them. Preserve explicit permission
grants during that migration rather than automatically expanding named roles.

On a catalog outage, consumers should retain a known, reviewed definition snapshot
and fail closed for unknown service ids or permissions. Do not replace the
permission universe with an empty allowlist or grant access because discovery is
unavailable. Existing uptime probes can continue from their last inventory.

## API and libraries

Mint a server-side reader with `gerry token create service-catalog --scope catalog-read`.
This scope can read every catalog identity, version and asset, but cannot access
allocation/port APIs or change routes, manifests, metadata or credentials. Owner
tokens cannot read the catalog. Admin/root credentials publish it.

| Method | Endpoint | Result |
|---|---|---|
| GET | `/v1/catalog?application_id=app:atlas` | Full snapshot; app filter optional |
| GET | `/v1/catalog/access?environment=production&workspace=` | `{schema_version, services: AccessEntry[]}` |
| POST | `/v1/catalog/apply` | Publish one typed `Bundle` |
| POST | `/v1/catalog/assets` | Upload `{data: base64}`; returns `{id, media_type}` |
| GET | `/v1/catalog/assets/{id}` | Authenticated image bytes, ETag and restrictive CSP |
| POST | `/v1/catalog/deployments/{id}/deactivate` | Retire deployment from discovery |

The Go client exposes `Snapshot`, `Access`, `Apply`, `UploadAsset` and `Deactivate`.
The Laravel client exposes `catalog()`, `accessCatalog($environment, $workspace)`
and `catalogAsset($id)`. Use backend credentials and proxy image bytes through the
consumer's authenticated origin; do not expose registry tokens in browser URLs.
Multiple registered deployments of a service can appear in the same scope;
consumers must group by service id or choose a deployment explicitly.

The infra portal and Olsyn Service Access can now adopt this contract. This update
does not migrate their existing configuration or switch their authorization.
Desktop routing views continue to use allocations; catalog metadata is available
to local tools through the CLI and the same API.

## Upgrade and verification

Deploy the updated daemon before updated clients. SQLite/PostgreSQL migrations
add catalog tables without modifying allocation, port or token records. The
catalog is backed by the same database; include it in normal Gerry backups.

The root CI runs `go vet ./...` and `go test -race ./...`. Vite logo-import tests
run with `npm ci && npm test` in `clients/vite`. Optional PostgreSQL catalog tests
use `GERRY_TEST_POSTGRES_DSN=... go test -race ./internal/store -run TestCatalog`;
they create and remove isolated schemas in the specified test database. CI also
runs those tests against PostgreSQL 16 and exercises the Laravel SDK with
`composer install && php tests/catalog.php` in `clients/laravel`.

## Uptime check definitions

Deployments can declare optional `checks` with a stable `id`, `name`, HTTP(S)
`url`, description, accepted `statuses` (default 200), `contains`, scalar `json`
expectations, and a `redirect` expectation (`origin` and YAML `path_prefix`;
JSON `pathPrefix`). Consumers choose which environments and networks to probe.
No check is inferred from a hostname or tenant allocation, and Gerry never runs
checks. Probe credentials remain with the monitoring consumer. Check ids must be
unique within each application bundle; consumers also reject collisions across
applications. Independent monitors should persist a last-known-good snapshot and
continue checks if the catalog is unavailable, while displaying discovery age.

Container builds publish immutable `ghcr.io/nano112/gerrymander:sha-<commit>`
tags for amd64 and arm64 after tests pass. Production deployments should pin a
reviewed image digest. This workflow does not change running clusters.
