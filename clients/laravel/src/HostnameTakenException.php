<?php

namespace Gerrymander;

class HostnameTakenException extends \RuntimeException
{
    /** @param string[] $suggestions */
    public function __construct(
        public readonly string $reason,
        string $message,
        public readonly array $suggestions = [],
    ) {
        parent::__construct($message);
    }
}
