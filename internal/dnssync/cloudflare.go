// Package dnssync reconciles DNS records at a provider from the registry —
// for zones that can't (or don't want to) rely on a single wildcard record.
//
// Safety contract, same shape as the k8s actuator: every record this
// package creates carries the comment "gerrymander-managed", and it will
// only ever update or delete records carrying that comment. Hand-created
// records are invisible to it.
package dnssync

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/Nano112/gerrymander/internal/store"
)

// ManagedComment marks records owned by gerry. The only records it touches.
const ManagedComment = "gerrymander-managed"

// CFZone maps one gerry zone onto one Cloudflare zone.
type CFZone struct {
	Zone     string // gerry zone name, e.g. example.com
	CFZoneID string // cloudflare zone id
	// Target for created records: CNAME when a hostname, A when an IP.
	Target  string
	Proxied bool
}

// Cloudflare reconciles per-label records for active allocations.
type Cloudflare struct {
	Store    *store.Store
	Token    string
	Zones    []CFZone
	Interval time.Duration
	Log      *slog.Logger
	// BaseURL overrides the API endpoint (tests). Default: the real API.
	BaseURL string

	http *http.Client
}

type cfRecord struct {
	ID      string `json:"id,omitempty"`
	Type    string `json:"type"`
	Name    string `json:"name"`
	Content string `json:"content"`
	TTL     int    `json:"ttl"`
	Proxied bool   `json:"proxied"`
	Comment string `json:"comment"`
}

func (c *Cloudflare) base() string {
	if c.BaseURL != "" {
		return c.BaseURL
	}
	return "https://api.cloudflare.com/client/v4"
}

func (c *Cloudflare) client() *http.Client {
	if c.http == nil {
		c.http = &http.Client{Timeout: 20 * time.Second}
	}
	return c.http
}

func (c *Cloudflare) do(ctx context.Context, method, path string, body, into any) error {
	var rd *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	} else {
		rd = bytes.NewReader(nil)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base()+path, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.client().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		var e struct {
			Errors []struct {
				Message string `json:"message"`
			} `json:"errors"`
		}
		json.NewDecoder(resp.Body).Decode(&e)
		msg := resp.Status
		if len(e.Errors) > 0 {
			msg = e.Errors[0].Message
		}
		return fmt.Errorf("%s %s: %s", method, path, msg)
	}
	if into != nil {
		var envelope struct {
			Result json.RawMessage `json:"result"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
			return err
		}
		return json.Unmarshal(envelope.Result, into)
	}
	return nil
}

// recordType picks CNAME for hostname targets, A for IPs.
func recordType(target string) string {
	if strings.Count(target, ".") >= 1 && strings.ContainsAny(target, "abcdefghijklmnopqrstuvwxyz") {
		return "CNAME"
	}
	return "A"
}

// desired returns the record set the registry implies for one CF zone.
func (c *Cloudflare) desired(ctx context.Context, z CFZone) (map[string]cfRecord, error) {
	allocs, err := c.Store.ListAllocations(ctx, store.AllocFilter{Zone: z.Zone, State: "active"})
	if err != nil {
		return nil, err
	}
	want := map[string]cfRecord{}
	for _, a := range allocs {
		name := a.FQDN // wildcard labels already carry the *. prefix
		if a.Label == "@" {
			name = z.Zone
		}
		want[name] = cfRecord{
			Type: recordType(z.Target), Name: name, Content: z.Target,
			TTL: 1 /* auto */, Proxied: z.Proxied, Comment: ManagedComment,
		}
	}
	return want, nil
}

// Reconcile converges managed records to the registry for every zone.
func (c *Cloudflare) Reconcile(ctx context.Context) error {
	for _, z := range c.Zones {
		want, err := c.desired(ctx, z)
		if err != nil {
			return err
		}
		var existing []cfRecord
		path := fmt.Sprintf("/zones/%s/dns_records?per_page=5000&comment=%s", z.CFZoneID, ManagedComment)
		if err := c.do(ctx, "GET", path, nil, &existing); err != nil {
			return fmt.Errorf("list %s: %w", z.Zone, err)
		}

		seen := map[string]bool{}
		for _, ex := range existing {
			if ex.Comment != ManagedComment {
				continue // defense in depth: never touch unmarked records
			}
			seen[ex.Name] = true
			w, ok := want[ex.Name]
			if !ok {
				if err := c.do(ctx, "DELETE", fmt.Sprintf("/zones/%s/dns_records/%s", z.CFZoneID, ex.ID), nil, nil); err != nil {
					c.logf("delete %s: %v", ex.Name, err)
				} else {
					c.logf("removed record %s (allocation released)", ex.Name)
				}
				continue
			}
			if ex.Type != w.Type || ex.Content != w.Content || ex.Proxied != w.Proxied {
				if err := c.do(ctx, "PATCH", fmt.Sprintf("/zones/%s/dns_records/%s", z.CFZoneID, ex.ID), w, nil); err != nil {
					c.logf("update %s: %v", ex.Name, err)
				} else {
					c.logf("repaired record %s (drift)", ex.Name)
				}
			}
		}
		for name, w := range want {
			if seen[name] {
				continue
			}
			if err := c.do(ctx, "POST", fmt.Sprintf("/zones/%s/dns_records", z.CFZoneID), w, nil); err != nil {
				c.logf("create %s: %v", name, err)
			} else {
				c.logf("created record %s → %s", name, w.Content)
			}
		}
	}
	return nil
}

// Run reconciles on an interval until ctx ends.
func (c *Cloudflare) Run(ctx context.Context) {
	if c.Interval <= 0 {
		c.Interval = 5 * time.Minute
	}
	t := time.NewTicker(c.Interval)
	defer t.Stop()
	for {
		if err := c.Reconcile(ctx); err != nil && ctx.Err() == nil {
			c.logf("reconcile: %v", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (c *Cloudflare) logf(format string, args ...any) {
	if c.Log != nil {
		c.Log.Info("dns-sync(cloudflare): " + fmt.Sprintf(format, args...))
	}
}
