package main

import (
	"context"
	"flag"
	"fmt"

	"github.com/Nano112/gerrymander/catalog"
	"github.com/Nano112/gerrymander/internal/manifest"
)

func catalogClient() *catalog.Client { c := apiClient(); return catalog.NewClient(c.Base, c.APIKey) }
func prepareManifest(ctx context.Context, path string) ([]byte, error) {
	return manifest.Prepare(path, func(data []byte) (string, error) { a, err := catalogClient().UploadAsset(ctx, data); return a.ID, err })
}

func cmdCatalog(args []string) error {
	f := flag.NewFlagSet("catalog", flag.ContinueOnError)
	app := f.String("application", "", "filter by stable application id")
	env := f.String("environment", "", "deployment environment (required with --access)")
	workspace := f.String("workspace", "", "exact workspace; empty selects shared deployments")
	access := f.Bool("access", false, "only explicitly activated capability definitions")
	if err := f.Parse(args); err != nil {
		return err
	}
	if f.NArg() != 0 {
		return fmt.Errorf("usage: gerry catalog [--application ID] [--environment ENV] [--workspace ID] [--access]")
	}
	ctx := context.Background()
	snapshot, err := catalogClient().Snapshot(ctx, *app)
	if err != nil {
		return err
	}
	if *access {
		if *env == "" {
			return fmt.Errorf("--access requires --environment")
		}
		printJSON(snapshot.Access(*env, *workspace))
		return nil
	}
	if *env != "" || *workspace != "" {
		deployments := snapshot.Deployments[:0]
		for _, d := range snapshot.Deployments {
			if (*env == "" || d.Environment == *env) && d.Workspace == *workspace {
				deployments = append(deployments, d)
			}
		}
		snapshot.Deployments = deployments
		resources := snapshot.Resources[:0]
		for _, r := range snapshot.Resources {
			if (*env == "" || r.Environment == *env) && r.Workspace == *workspace {
				resources = append(resources, r)
			}
		}
		snapshot.Resources = resources
	}
	printJSON(snapshot)
	return nil
}
