# Scoped tokens

The root API key can do everything. That's fine for you; it's wrong for the
tenant self-service portal, the CI job, or the neighboring team's service.
Scoped tokens are the separation:

```bash
gerry token create tenant-acme --owner-ref acme-uuid --zone yourdomain.com
# token tenant-acme (owner, owner acme-uuid, zones yourdomain.com)
#
#   gk_9f2c…                       ← shown exactly once
```

## What an owner-scoped token can do

| Operation | Behavior |
|---|---|
| claim | forced to the token's `owner_ref`, kind `tenant`, allowed zones only |
| list | sees only its own allocations |
| get / rename / renew / release | own allocations only, 403 otherwise |
| patch | `spec` only, never state or labels |
| audit | `GET /v1/allocations/{id}/events` for its own allocations |
| everything else | 403: zones, ports, manifest apply, processes, conflicts, token management |

Every rule above is pinned by a test
(`internal/api/scopes_test.go`). The scope model is part of the public
contract, not an implementation detail.

## Mechanics

- Plaintext is `gk_` + 48 hex chars, shown once at creation. Only the
  SHA-256 is stored.
- `gerry token revoke <name>` disables it immediately and permanently;
  mint a new one to restore access.
- `gerry token ls` shows scope, owner, zones, last use, and revocation
  state (metadata only, never secrets).
- The root API key keeps working unchanged; keyless loopback dev mode is
  unaffected.

## Audit trail

Every allocation carries an append-only event history: claimed, renamed,
renewed, released, by whom, from what state.

```bash
curl -H "Authorization: Bearer $TOKEN" \
  https://gerry.yourdomain.com/v1/allocations/42/events
```

Owner tokens see their own allocations' history; admins see everything.
