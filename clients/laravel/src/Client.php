<?php

namespace Gerrymander;

use Illuminate\Support\Facades\Http;

/**
 * Thin client for the gerrymander API.
 *
 * Config (config/gerrymander.php or env):
 *   GERRY_API      base URL, e.g. http://gerrymander.gerrymander.svc:4780
 *   GERRY_API_KEY  bearer token
 *   GERRY_ZONE     default zone, e.g. olsyn.com
 *
 * With GERRY_API unset the client is inert — see enabled(). That is the
 * supported way to run the app in CI, in tests, or on a machine where the
 * daemon was never started.
 */
class Client
{
    private string $base;

    private ?string $apiKey;

    public function __construct(?string $base = null, ?string $apiKey = null)
    {
        $this->base = rtrim($base ?? (string) config('gerrymander.api'), '/');
        $this->apiKey = $apiKey ?? config('gerrymander.key');
    }

    /**
     * Whether the registry is configured at all. Guard integration points
     * with this rather than try/catch: an unconfigured registry is a
     * deployment choice, not an error.
     */
    public function enabled(): bool
    {
        return $this->base !== '' && (bool) config('gerrymander.enabled');
    }

    private function http()
    {
        $req = Http::timeout(5)->acceptJson();
        if ($this->apiKey) {
            $req = $req->withToken($this->apiKey);
        }

        return $req;
    }

    /**
     * @return array{available: bool, reason?: string, message?: string, suggestions?: string[]}
     */
    public function availability(string $zone, string $label): array
    {
        return $this->http()
            ->get("{$this->base}/v1/zones/{$zone}/availability", ['label' => $label])
            ->throw()
            ->json();
    }

    /**
     * Route spec for tenant claims when a k8s backend is configured; null
     * means registry-only claims, leaving the catch-all to route.
     *
     * @return array{routes: array<int, array<string, mixed>>}|null
     */
    public function tenantRouteSpec(): ?array
    {
        $b = (array) config('gerrymander.backend');
        if (empty($b['namespace']) || empty($b['service'])) {
            return null;
        }

        return ['routes' => [['backend' => ['kind' => 'service', 'service' => [
            'namespace' => $b['namespace'],
            'name' => $b['service'],
            'port' => (int) ($b['port'] ?? 80),
        ]]]]];
    }

    /** Claim a hostname; returns the allocation array. Throws on conflict. */
    public function claim(string $zone, string $label, array $opts = []): array
    {
        $resp = $this->http()->post("{$this->base}/v1/claims", array_merge([
            'zone' => $zone,
            'label' => $label,
        ], $opts));

        if ($resp->status() === 409) {
            throw new HostnameTakenException(
                $resp->json('error') ?? 'taken',
                $resp->json('message') ?? "{$label} is unavailable",
                $resp->json('suggestions') ?? [],
            );
        }

        return $resp->throw()->json();
    }

    /** Hold a hostname for the given TTL while provisioning completes. */
    public function hold(string $zone, string $label, string $ttl = '15m', array $opts = []): array
    {
        return $this->claim($zone, $label, array_merge($opts, ['hold' => true, 'hold_ttl' => $ttl]));
    }

    /** Promote a hold to an active allocation. */
    public function commit(int $allocationId): array
    {
        return $this->http()->post("{$this->base}/v1/allocations/{$allocationId}/commit")->throw()->json();
    }

    /** Release an allocation. */
    public function release(int $allocationId): void
    {
        $this->http()->delete("{$this->base}/v1/allocations/{$allocationId}")->throw();
    }

    /**
     * Every allocation in a zone — the platform-admin view.
     *
     * @return array<int, array<string, mixed>>
     */
    public function allocations(string $zone): array
    {
        return $this->http()
            ->get("{$this->base}/v1/allocations", ['zone' => $zone])
            ->throw()
            ->json('allocations') ?? [];
    }

    /**
     * Allocations one owner holds in a zone. This is what makes claiming
     * idempotent: on a 409, ask whether the conflict is with yourself.
     *
     * @return array<int, array<string, mixed>>
     */
    public function allocationsFor(string $zone, string $ownerRef): array
    {
        return $this->http()
            ->get("{$this->base}/v1/allocations", ['zone' => $zone, 'owner_ref' => $ownerRef])
            ->throw()
            ->json('allocations') ?? [];
    }

    /** Patch an allocation (spec, labels, state). */
    public function update(int $allocationId, array $patch): array
    {
        return $this->http()
            ->patch("{$this->base}/v1/allocations/{$allocationId}", $patch)
            ->throw()
            ->json('allocation') ?? [];
    }

    /** Rename an allocation atomically, keeping id/owner/routes/history. */
    public function rename(int $allocationId, string $label): array
    {
        $resp = $this->http()->post("{$this->base}/v1/allocations/{$allocationId}/rename", ['label' => $label]);

        if ($resp->status() === 409) {
            throw new HostnameTakenException(
                $resp->json('error') ?? 'taken',
                $resp->json('message') ?? "{$label} is unavailable",
                $resp->json('suggestions') ?? [],
            );
        }

        return $resp->throw()->json();
    }

    /** Sticky port claim: the same owner_ref always receives the same port. */
    public function port(string $ownerRef, string $pool = 'dev'): int
    {
        return (int) $this->http()
            ->post("{$this->base}/v1/ports", ['pool' => $pool, 'owner_ref' => $ownerRef])
            ->throw()
            ->json('value');
    }
}
