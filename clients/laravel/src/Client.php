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
 */
class Client
{
    public function __construct(
        private ?string $base = null,
        private ?string $apiKey = null,
    ) {
        $this->base = rtrim($base ?? config('gerrymander.api', env('GERRY_API', 'http://127.0.0.1:4780')), '/');
        $this->apiKey = $apiKey ?? config('gerrymander.key', env('GERRY_API_KEY'));
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

    /** Sticky port claim: the same owner_ref always receives the same port. */
    public function port(string $ownerRef, string $pool = 'dev'): int
    {
        return (int) $this->http()
            ->post("{$this->base}/v1/ports", ['pool' => $pool, 'owner_ref' => $ownerRef])
            ->throw()
            ->json('value');
    }
}
