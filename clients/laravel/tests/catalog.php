<?php

// Run with the package's composer dependencies, or pass another Laravel autoloader.
require $argv[1] ?? dirname(__DIR__).'/vendor/autoload.php';
require_once dirname(__DIR__).'/src/Client.php';

use Gerrymander\Client;
use Illuminate\Http\Client\Factory;
use Illuminate\Support\Facades\Http;

function check(bool $condition, string $message): void
{
    if (!$condition) {
        throw new RuntimeException($message);
    }
}

Http::swap(new Factory);
Http::fake([
    'https://registry.example/v1/catalog/access*' => Http::response(['schema_version' => 1, 'services' => [['service' => ['id' => 'console']]]]),
    'https://registry.example/v1/catalog/assets/*' => Http::response('<svg/>', 200, ['Content-Type' => 'image/svg+xml']),
    'https://registry.example/v1/catalog*' => Http::response(['schema_version' => 1, 'applications' => [['id' => 'app:demo']]]),
]);
$client = new Client('https://registry.example', 'test-reader');
check($client->catalog('app:demo')['applications'][0]['id'] === 'app:demo', 'Catalog identity lost');
check($client->accessCatalog('production', 'shared')[0]['service']['id'] === 'console', 'Access service identity lost');
$requests = Http::recorded();
check($requests[1][0]['environment'] === 'production' && $requests[1][0]['workspace'] === 'shared', 'Scope filters missing');
check($requests[1][0]->hasHeader('Authorization', 'Bearer test-reader'), 'Reader credential missing');
$asset = $client->catalogAsset('sha256:'.str_repeat('a', 64));
check($asset['data'] === '<svg/>' && $asset['content_type'] === 'image/svg+xml', 'Asset response lost');

try {
    $client->accessCatalog('');
    throw new RuntimeException('Empty environment accepted');
} catch (InvalidArgumentException $expected) {
}
try {
    $client->catalogAsset('../secret');
    throw new RuntimeException('Invalid asset path accepted');
} catch (InvalidArgumentException $expected) {
}
Http::swap(new Factory);
Http::fake(['*' => Http::response([], 503)]);
try {
    $client->accessCatalog('production');
    throw new RuntimeException('Catalog outage silently returned an empty definition');
} catch (Illuminate\Http\Client\RequestException $expected) {
}
Http::swap(new Factory);
Http::fake(['*' => Http::response(['schema_version' => 999, 'services' => []])]);
try {
    $client->accessCatalog('production');
    throw new RuntimeException('Unsupported schema accepted');
} catch (UnexpectedValueException $expected) {
}
echo "Catalog SDK tests passed\n";
