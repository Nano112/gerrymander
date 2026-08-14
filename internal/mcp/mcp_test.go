package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Nano112/gerrymander/internal/api"
	"github.com/Nano112/gerrymander/internal/client"
	"github.com/Nano112/gerrymander/internal/core"
	"github.com/Nano112/gerrymander/internal/service"
	"github.com/Nano112/gerrymander/internal/store"
)

func TestMCPGoldenFlow(t *testing.T) {
	st, err := store.Open("sqlite:" + filepath.Join(t.TempDir(), "mcp.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ports := service.NewPorts(st)
	ports.SkipBindTest = true
	alloc := service.NewAlloc(st, ports)
	ctx := context.Background()
	st.EnsureZone(ctx, core.Zone{Name: "olsyn.test"})
	ports.EnsureDefaultPool(ctx)
	ts := httptest.NewServer((&api.Server{Store: st, Alloc: alloc, Ports: ports}).Handler())
	defer ts.Close()

	in := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"claim_port","arguments":{"owner_ref":"agent-proj"}}}`,
		`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"claim_port","arguments":{"owner_ref":"agent-proj"}}}`,
		`{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"check_availability","arguments":{"zone":"olsyn.test","label":"grafana"}}}`,
	}, "\n") + "\n"
	var out bytes.Buffer
	s := &Server{API: client.New(ts.URL, ""), In: strings.NewReader(in), Out: &out}
	if err := s.Run(context.Background()); err != nil {
		t.Fatal(err)
	}

	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 5 {
		t.Fatalf("want 5 responses, got %d: %s", len(lines), out.String())
	}
	var init map[string]any
	json.Unmarshal([]byte(lines[0]), &init)
	if init["result"].(map[string]any)["serverInfo"].(map[string]any)["name"] != "gerrymander" {
		t.Fatalf("initialize: %s", lines[0])
	}
	var list map[string]any
	json.Unmarshal([]byte(lines[1]), &list)
	toolList := list["result"].(map[string]any)["tools"].([]any)
	if len(toolList) != 11 {
		t.Fatalf("tools/list: %d tools", len(toolList))
	}
	// claim_port twice → same sticky value
	value := func(line string) string {
		var m map[string]any
		json.Unmarshal([]byte(line), &m)
		text := m["result"].(map[string]any)["content"].([]any)[0].(map[string]any)["text"].(string)
		var pa map[string]any
		json.Unmarshal([]byte(text), &pa)
		return string(rune(int(pa["value"].(float64))))
	}
	if value(lines[2]) != value(lines[3]) {
		t.Fatalf("sticky port differs across calls: %s vs %s", lines[2], lines[3])
	}
	// availability of blocked label mentions reason
	if !strings.Contains(lines[4], "blocked") {
		t.Fatalf("availability: %s", lines[4])
	}
}
