package main

import (
	"context"
	"flag"
	"fmt"
	"strings"
)

// cmdToken manages scoped API tokens: credentials that let a tenant (or a
// CI job, or another service) talk to the registry without holding the root
// key. Owner-scoped tokens can only claim/rename/release their own
// hostnames; admin-scoped ones mirror the root key.
func cmdToken(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf(`usage:
  gerry token create <name> [--scope owner|admin] [--owner-ref REF] [--zone Z]...
  gerry token ls
  gerry token revoke <name>`)
	}
	sub, rest := args[0], args[1:]
	c := apiClient()
	ctx := context.Background()

	switch sub {
	case "create":
		fs := flag.NewFlagSet("token create", flag.ExitOnError)
		scope := fs.String("scope", "owner", "owner (confined to --owner-ref) or admin")
		ownerRef := fs.String("owner-ref", "", "owner this token acts as (required for owner scope)")
		var zones multiFlag
		fs.Var(&zones, "zone", "zone the token may touch (repeatable; empty = all)")
		// allow `gerry token create NAME --flags` (name before flags)
		var name string
		if len(rest) > 0 && !strings.HasPrefix(rest[0], "-") {
			name, rest = rest[0], rest[1:]
		}
		fs.Parse(rest)
		if name == "" {
			return fmt.Errorf("token create: a name is required")
		}
		var out struct {
			Token     map[string]any `json:"token"`
			Plaintext string         `json:"plaintext"`
		}
		err := c.Do(ctx, "POST", "/v1/tokens", map[string]any{
			"name": name, "scope": *scope, "owner_ref": *ownerRef, "zones": []string(zones),
		}, &out)
		if err != nil {
			return err
		}
		fmt.Printf("token %s (%s", name, *scope)
		if *ownerRef != "" {
			fmt.Printf(", owner %s", *ownerRef)
		}
		if len(zones) > 0 {
			fmt.Printf(", zones %s", strings.Join(zones, ","))
		}
		fmt.Println(")")
		fmt.Println()
		fmt.Println("  " + out.Plaintext)
		fmt.Println()
		fmt.Println("This is the only time the plaintext is shown — store it now.")
		return nil

	case "ls":
		var out struct {
			Tokens []struct {
				Name       string   `json:"name"`
				Scope      string   `json:"scope"`
				OwnerRef   string   `json:"owner_ref"`
				Zones      []string `json:"zones"`
				LastUsedAt *string  `json:"last_used_at"`
				RevokedAt  *string  `json:"revoked_at"`
			} `json:"tokens"`
		}
		if err := c.Do(ctx, "GET", "/v1/tokens", nil, &out); err != nil {
			return err
		}
		if len(out.Tokens) == 0 {
			fmt.Println("no tokens (create one: gerry token create <name> --owner-ref <ref>)")
			return nil
		}
		for _, t := range out.Tokens {
			status := "live"
			if t.RevokedAt != nil {
				status = "revoked"
			}
			extra := ""
			if t.OwnerRef != "" {
				extra = " owner=" + t.OwnerRef
			}
			if len(t.Zones) > 0 {
				extra += " zones=" + strings.Join(t.Zones, ",")
			}
			last := "never used"
			if t.LastUsedAt != nil {
				last = "last used " + *t.LastUsedAt
			}
			fmt.Printf("%-20s %-6s %-8s%s (%s)\n", t.Name, t.Scope, status, extra, last)
		}
		return nil

	case "revoke":
		if len(rest) != 1 {
			return fmt.Errorf("usage: gerry token revoke <name>")
		}
		if err := c.Do(ctx, "DELETE", "/v1/tokens/"+rest[0], nil, nil); err != nil {
			return err
		}
		fmt.Println("revoked", rest[0], "(permanent — mint a new token to restore access)")
		return nil

	default:
		return fmt.Errorf("unknown token subcommand %q (create|ls|revoke)", sub)
	}
}

type multiFlag []string

func (m *multiFlag) String() string     { return strings.Join(*m, ",") }
func (m *multiFlag) Set(v string) error { *m = append(*m, v); return nil }
