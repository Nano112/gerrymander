<?php

namespace Gerrymander\Rules;

use Closure;
use Gerrymander\Client;
use Illuminate\Contracts\Validation\ValidationRule;

/**
 * Validation rule: the label must be claimable in the zone.
 *
 * Fails CLOSED: when the gerrymander API is unreachable, the embedded
 * blocklist still rejects the obviously-reserved names and everything else
 * is rejected with a retryable message — an outage must never hand out
 * "grafana".
 *
 * Usage:
 *   'subdomain' => ['required', new HostnameAvailable('olsyn.com')]
 */
class HostnameAvailable implements ValidationRule
{
    /** Embedded copy of gerrymander's builtin blocklists (offline fallback). */
    private const BLOCKED = [
        // rfc2142
        'www', 'mail', 'smtp', 'pop', 'pop3', 'imap', 'ftp', 'webmaster', 'postmaster',
        'hostmaster', 'usenet', 'news', 'abuse', 'noc', 'security', 'sales', 'support',
        'marketing', 'info',
        // common
        'admin', 'administrator', 'api', 'app', 'apps', 'assets', 'auth', 'autoconfig',
        'autodiscover', 'backup', 'beta', 'billing', 'blog', 'cdn', 'chat', 'ci', 'cloud',
        'cname', 'console', 'cpanel', 'dashboard', 'db', 'demo', 'dev', 'developer',
        'developers', 'dns', 'dns1', 'dns2', 'docs', 'download', 'downloads', 'email',
        'files', 'forum', 'git', 'grafana', 'help', 'helpdesk', 'home', 'host', 'hub',
        'internal', 'intranet', 'localhost', 'login', 'logout', 'logs', 'm', 'media',
        'metrics', 'monitor', 'monitoring', 'mx', 'mysql', 'ns', 'ns1', 'ns2', 'ns3',
        'ns4', 'oauth', 'official', 'old', 'ops', 'owa', 'panel', 'payment', 'payments',
        'portal', 'postgres', 'private', 'prod', 'production', 'prometheus', 'proxy',
        'redis', 'register', 'registry', 'root', 'router', 'search', 'secure', 'server',
        'service', 'services', 'shop', 'signin', 'signup', 'site', 'ssh', 'ssl', 'stage',
        'staging', 'stats', 'status', 'store', 'stream', 'svn', 'sysadmin', 'system',
        'test', 'testing', 'tls', 'uat', 'upload', 'uploads', 'user', 'users', 'vpn',
        'webdisk', 'webhook', 'webhooks', 'webmail', 'wiki', 'wpad',
    ];

    public function __construct(
        private ?string $zone = null,
        private ?Client $client = null,
    ) {
        $this->zone = $zone ?? config('gerrymander.zone', env('GERRY_ZONE'));
        $this->client = $client ?? new Client;
    }

    public function validate(string $attribute, mixed $value, Closure $fail): void
    {
        $label = strtolower(trim((string) $value));

        if (in_array($label, self::BLOCKED, true)) {
            $fail("The {$attribute} \"{$label}\" is reserved.");

            return;
        }

        try {
            $result = $this->client->availability($this->zone, $label);
        } catch (\Throwable) {
            // Fail closed: without the registry we cannot prove uniqueness.
            $fail("Subdomain availability cannot be verified right now — please try again shortly.");

            return;
        }

        if (! ($result['available'] ?? false)) {
            $msg = "The {$attribute} \"{$label}\" is not available.";
            if (! empty($result['suggestions'])) {
                $msg .= ' Try: '.implode(', ', $result['suggestions']).'.';
            }
            $fail($msg);
        }
    }
}
