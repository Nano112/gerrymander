<?php

return [
    // Base URL of the gerrymander API. In-cluster this is the service DNS
    // (http://gerrymander.gerrymander.svc:4780); on a dev machine it is the
    // local daemon, which containers reach via api.extra_listen: ["@docker"].
    //
    // Deliberately no default: an unset GERRY_API is how you turn the whole
    // integration off (see 'enabled'), and a default would make that
    // impossible to express.
    'api' => env('GERRY_API'),

    // Bearer token. Empty for a loopback daemon.
    'key' => env('GERRY_API_KEY'),

    // Default zone for availability checks and claims (olsyn.com in prod,
    // olsyn.test locally).
    'zone' => env('GERRY_ZONE'),

    // Master switch. With no API configured every Client call is a no-op, so
    // the same code runs in CI and in local setups that never started the
    // daemon without guarding each call site.
    'enabled' => (bool) env('GERRY_API'),

    // Kubernetes Service that tenant hostnames route to. When set, claims
    // carry a service backend and the actuator materializes an explicit
    // per-tenant IngressRoute (priority-floored above the catch-all, same
    // upstream). Unset = registry-only claims, and whatever catch-all you
    // already have keeps routing — which is what you want locally, where one
    // wildcard in gerrymander.yaml covers every tenant.
    'backend' => [
        'namespace' => env('GERRY_BACKEND_NAMESPACE'),
        'service' => env('GERRY_BACKEND_SERVICE'),
        'port' => (int) env('GERRY_BACKEND_PORT', 80),
    ],
];
