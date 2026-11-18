package main

import (
	"context"
	"embed"
	"fmt"
	"log"
	"net"
	"os/exec"
	"time"

	"github.com/Nano112/gerrymander/internal/rexlink"
	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/mac"
)

//go:embed all:frontend/dist
var assets embed.FS

func main() {
	app := NewApp()

	err := wails.Run(&options.App{
		Title:  "Gerrymander",
		Width:  1080,
		Height: 760,
		AssetServer: &assetserver.Options{
			Assets: assets,
		},
		BackgroundColour: &options.RGBA{R: 18, G: 18, B: 22, A: 1},
		OnStartup:        app.startup,
		Bind: []interface{}{
			app,
		},
		Mac: &mac.Options{
			OnUrlOpen: func(raw string) {
				if err := openRexLink(raw); err != nil {
					log.Printf("open Gerry link: %v", err)
				}
			},
			TitleBar:             mac.TitleBarHiddenInset(),
			Appearance:           mac.NSAppearanceNameDarkAqua,
			WebviewIsTransparent: false,
			About: &mac.AboutInfo{
				Title:   "Gerrymander",
				Message: "Hostname and port control plane\ngithub.com/Nano112/gerrymander",
			},
		},
	})
	if err != nil {
		panic(err)
	}
}

func openRexLink(raw string) error {
	hosts, err := rexlink.Load()
	if err != nil {
		return err
	}
	target, err := hosts.BrowserURL(raw)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	ips, err := net.DefaultResolver.LookupIPAddr(ctx, target.Hostname())
	if err != nil {
		return err
	}
	if len(ips) == 0 {
		return fmt.Errorf("no local DNS record for %s", target.Host)
	}
	for _, ip := range ips {
		if !ip.IP.IsLoopback() {
			return fmt.Errorf("%s must resolve to loopback on this Mac", target.Host)
		}
	}
	return exec.Command("/usr/bin/open", target.String()).Run()
}
