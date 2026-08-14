<?php

return [
    // Base URL of the gerrymander API.
    'api' => env('GERRY_API', 'http://127.0.0.1:4780'),

    // Bearer token.
    'key' => env('GERRY_API_KEY'),

    // Default zone for availability checks and claims.
    'zone' => env('GERRY_ZONE'),
];
