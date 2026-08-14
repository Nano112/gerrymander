<?php

namespace Gerrymander\Console;

use Gerrymander\Client;
use Gerrymander\HostnameTakenException;
use Illuminate\Console\Command;

/**
 * Backfill existing tenants into the gerrymander registry.
 *
 * Expects stancl/tenancy's Domain model; override --model for other setups.
 * Idempotent: already-registered labels are reported and skipped.
 */
class HostnameSyncCommand extends Command
{
    protected $signature = 'hostname:sync
        {--zone= : Zone to sync into (default: config gerrymander.zone)}
        {--model=\\Stancl\\Tenancy\\Database\\Models\\Domain : Domain model class}
        {--dry-run : Report without claiming}';

    protected $description = 'Register existing tenant domains with gerrymander';

    public function handle(Client $client): int
    {
        $zone = $this->option('zone') ?? config('gerrymander.zone');
        if (! $zone) {
            $this->error('No zone configured (GERRY_ZONE / --zone).');

            return self::FAILURE;
        }
        $model = $this->option('model');
        if (! class_exists($model)) {
            $this->error("Model {$model} not found.");

            return self::FAILURE;
        }

        $suffix = '.'.$zone;
        $synced = $skipped = $foreign = 0;
        foreach ($model::query()->cursor() as $domain) {
            $host = strtolower($domain->domain);
            if (! str_ends_with($host, $suffix)) {
                $foreign++;
                continue; // custom domain or other zone — not ours to claim
            }
            $label = substr($host, 0, -strlen($suffix));
            if ($this->option('dry-run')) {
                $this->line("would claim {$label} (tenant {$domain->tenant_id})");
                $synced++;
                continue;
            }
            try {
                $client->claim($zone, $label, [
                    'kind' => 'tenant',
                    'source' => 'seed',
                    'owner_ref' => (string) $domain->tenant_id,
                    'owner_kind' => 'tenant',
                ]);
                $this->info("claimed {$label}");
                $synced++;
            } catch (HostnameTakenException) {
                $this->line("exists  {$label}");
                $skipped++;
            }
        }
        $this->info("Done: {$synced} synced, {$skipped} already present, {$foreign} out-of-zone.");

        return self::SUCCESS;
    }
}
