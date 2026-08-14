<?php

namespace Gerrymander;

use Gerrymander\Console\HostnameSyncCommand;
use Illuminate\Support\ServiceProvider;

class GerrymanderServiceProvider extends ServiceProvider
{
    public function register(): void
    {
        $this->mergeConfigFrom(__DIR__.'/../config/gerrymander.php', 'gerrymander');
        $this->app->singleton(Client::class, fn () => new Client);
    }

    public function boot(): void
    {
        $this->publishes([
            __DIR__.'/../config/gerrymander.php' => config_path('gerrymander.php'),
        ], 'gerrymander-config');

        if ($this->app->runningInConsole()) {
            $this->commands([HostnameSyncCommand::class]);
        }
    }
}
