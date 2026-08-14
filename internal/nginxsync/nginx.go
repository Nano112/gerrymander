// Package nginxsync renders registry allocations into an nginx include
// file — for machines where nginx, not gerry's embedded proxy, is the
// dataplane. gerry stays the authority; nginx serves.
//
// Safety contract, same shape as every other writer: the include file
// starts with a marker line, and the reconciler refuses to overwrite any
// file that exists without it. Nothing outside that one file is touched;
// your nginx.conf, other server blocks, and certificates are invisible
// to it.
package nginxsync

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"

	"github.com/Nano112/gerrymander/internal/core"
	"github.com/Nano112/gerrymander/internal/store"
)

// Marker is the first line of every file this package owns.
const Marker = "# gerrymander-managed — edits are overwritten; remove nginx_sync from the config to release this file"

type Sync struct {
	Store    *store.Store
	Zones    []string
	ConfPath string
	// Listen directive for generated servers, e.g. "80" or
	// "443 ssl" (bring your own ssl_certificate via nginx's own config).
	Listen string
	// ReloadCmd runs after the file changes, e.g. "nginx -s reload".
	// Empty = write only.
	ReloadCmd string
	Interval  time.Duration
	Log       *slog.Logger
}

// Render produces the include file's content from the registry: one
// server block per active allocation with an address backend. Other
// backend kinds (service, supervised, docker) belong to other dataplanes
// and are skipped.
func (n *Sync) Render(ctx context.Context) (string, int, error) {
	var b strings.Builder
	b.WriteString(Marker + "\n")
	b.WriteString("# generated from the gerrymander registry — `gerry ls` is the source of truth\n\n")

	count := 0
	for _, zone := range n.Zones {
		allocs, err := n.Store.ListAllocations(ctx, store.AllocFilter{Zone: zone, State: string(core.StateActive)})
		if err != nil {
			return "", 0, err
		}
		sort.Slice(allocs, func(i, j int) bool { return allocs[i].FQDN < allocs[j].FQDN })
		for _, al := range allocs {
			if len(al.Spec.Routes) == 0 {
				continue
			}
			be := al.Spec.Routes[0].Backend
			if be.Kind != "address" || be.Address == nil {
				continue
			}
			host := be.Address.Host
			if host == "@local" {
				host = "127.0.0.1"
			}
			names := al.FQDN
			if al.Spec.Wildcard {
				names += " *." + al.FQDN
			}
			fmt.Fprintf(&b, "server {\n")
			fmt.Fprintf(&b, "    listen %s;\n", n.Listen)
			fmt.Fprintf(&b, "    server_name %s;\n", names)
			fmt.Fprintf(&b, "    location / {\n")
			fmt.Fprintf(&b, "        proxy_pass http://%s:%d;\n", host, be.Address.Port)
			fmt.Fprintf(&b, "        proxy_set_header Host $host;\n")
			fmt.Fprintf(&b, "        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;\n")
			fmt.Fprintf(&b, "        proxy_set_header X-Forwarded-Proto $scheme;\n")
			fmt.Fprintf(&b, "        proxy_http_version 1.1;\n")
			fmt.Fprintf(&b, "        proxy_set_header Upgrade $http_upgrade;\n")
			fmt.Fprintf(&b, "        proxy_set_header Connection \"upgrade\";\n")
			fmt.Fprintf(&b, "    }\n")
			fmt.Fprintf(&b, "}\n\n")
			count++
		}
	}
	return b.String(), count, nil
}

// Reconcile writes the include file when its content changed, then
// reloads nginx. A pre-existing file without the marker aborts with an
// error and is never overwritten.
func (n *Sync) Reconcile(ctx context.Context) error {
	content, count, err := n.Render(ctx)
	if err != nil {
		return err
	}
	if prev, err := os.ReadFile(n.ConfPath); err == nil {
		if !strings.HasPrefix(string(prev), "# gerrymander-managed") {
			return fmt.Errorf("%s exists without the gerry marker — refusing to overwrite a file gerry does not own", n.ConfPath)
		}
		if string(prev) == content {
			return nil // steady state
		}
	}
	if err := os.WriteFile(n.ConfPath, []byte(content), 0o644); err != nil {
		return err
	}
	n.logf("wrote %s (%d server blocks)", n.ConfPath, count)
	if n.ReloadCmd == "" {
		return nil
	}
	parts := strings.Fields(n.ReloadCmd)
	if out, err := exec.CommandContext(ctx, parts[0], parts[1:]...).CombinedOutput(); err != nil {
		return fmt.Errorf("reload (%s): %v: %s", n.ReloadCmd, err, strings.TrimSpace(string(out)))
	}
	n.logf("nginx reloaded")
	return nil
}

// Run reconciles on an interval until ctx ends.
func (n *Sync) Run(ctx context.Context) {
	if n.Interval <= 0 {
		n.Interval = 15 * time.Second
	}
	t := time.NewTicker(n.Interval)
	defer t.Stop()
	for {
		if err := n.Reconcile(ctx); err != nil && ctx.Err() == nil {
			n.logf("reconcile: %v", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (n *Sync) logf(format string, args ...any) {
	if n.Log != nil {
		n.Log.Info("nginx-sync: " + fmt.Sprintf(format, args...))
	}
}
