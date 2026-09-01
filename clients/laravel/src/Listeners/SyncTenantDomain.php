<?php

namespace Gerrymander\Listeners;

use Gerrymander\Client;
use Gerrymander\HostnameTakenException;
use Illuminate\Support\Facades\Log;
use Stancl\Tenancy\Events\CreatingDomain;
use Stancl\Tenancy\Events\DomainDeleted;

/**
 * Keeps the gerrymander registry in lockstep with stancl/tenancy's domains
 * table. Fires on every domain-creation path, because they all end at
 * Domain::create() — onboarding controllers, admin panels, seeders alike.
 *
 * Opt in from an EventServiceProvider (or a Event::listen in a provider):
 *
 *   Event::listen(CreatingDomain::class, [SyncTenantDomain::class, 'handleCreating']);
 *   Event::listen(DomainDeleted::class,  [SyncTenantDomain::class, 'handleDeleted']);
 *
 * It is not registered automatically: not every app wants domain creation to
 * be able to fail on a registry conflict, and that has to be a decision.
 *
 * Semantics:
 * - A REAL conflict ("taken"/"reserved" by someone else) aborts domain
 *   creation — that is the whole point of the registry.
 * - The same tenant re-claiming its own label is idempotent and passes.
 * - Registry connectivity failures log and continue: the HostnameAvailable
 *   validation rule already failed closed at the form, and blocking every
 *   admin flow on a registry outage is worse than the tiny race window.
 * - Domains outside the configured zone (custom domains, .test locals when
 *   the zone is olsyn.com) and multi-level labels are skipped.
 */
class SyncTenantDomain
{
    public function __construct(private Client $gerry = new Client) {}

    public function handleCreating(CreatingDomain $event): void
    {
        if (! $this->gerry->enabled()) {
            return;
        }
        $zone = (string) config('gerrymander.zone');
        $domain = strtolower($event->domain->domain);
        $tenantId = (string) $event->domain->tenant_id;

        if ($zone === '' || ! str_ends_with($domain, '.'.$zone)) {
            return; // custom domain or other zone — not the registry's problem
        }
        $label = substr($domain, 0, -strlen('.'.$zone));
        if ($label === '' || str_contains($label, '.')) {
            return; // legacy multi-level forms (gv.app.…) live under platform wildcards
        }

        try {
            $opts = [
                'kind' => 'tenant',
                'source' => 'api',
                'owner_ref' => $tenantId,
                'owner_kind' => 'tenant',
            ];
            if ($spec = $this->gerry->tenantRouteSpec()) {
                $opts['spec'] = $spec;
            }
            $this->gerry->claim($zone, $label, $opts);
        } catch (HostnameTakenException $e) {
            // Idempotency: the same tenant already owns this label.
            try {
                foreach ($this->gerry->allocationsFor($zone, $tenantId) as $alloc) {
                    if (($alloc['label'] ?? null) === $label) {
                        return;
                    }
                }
            } catch (\Throwable) {
                // fall through to the conflict below
            }
            throw new \InvalidArgumentException(
                "Hostname '{$label}.{$zone}' is {$e->reason} in the registry: {$e->getMessage()}"
            );
        } catch (\Throwable $e) {
            Log::warning('gerrymander claim skipped (registry unreachable)', [
                'domain' => $domain, 'tenant' => $tenantId, 'error' => $e->getMessage(),
            ]);
        }
    }

    public function handleDeleted(DomainDeleted $event): void
    {
        if (! $this->gerry->enabled()) {
            return;
        }
        $zone = (string) config('gerrymander.zone');
        $domain = strtolower($event->domain->domain);
        $tenantId = (string) $event->domain->tenant_id;

        if ($zone === '' || ! str_ends_with($domain, '.'.$zone)) {
            return;
        }
        $label = substr($domain, 0, -strlen('.'.$zone));
        if ($label === '' || str_contains($label, '.')) {
            return;
        }

        try {
            foreach ($this->gerry->allocationsFor($zone, $tenantId) as $alloc) {
                if (($alloc['label'] ?? null) === $label && isset($alloc['id'])) {
                    $this->gerry->release((int) $alloc['id']);

                    return;
                }
            }
        } catch (\Throwable $e) {
            Log::warning('gerrymander release skipped', [
                'domain' => $domain, 'tenant' => $tenantId, 'error' => $e->getMessage(),
            ]);
        }
    }
}
