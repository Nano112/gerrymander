// Package mcp exposes gerrymander to coding agents over the Model Context
// Protocol (JSON-RPC 2.0 on stdio). It is a thin adapter over the REST API.
package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/Nano112/gerrymander/internal/client"
)

// Server speaks MCP on (in, out).
type Server struct {
	API *client.Client
	In  io.Reader
	Out io.Writer
}

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type toolDef struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

func obj(props map[string]any, required ...string) map[string]any {
	if required == nil {
		required = []string{}
	}
	return map[string]any{"type": "object", "properties": props, "required": required}
}

func str(desc string) map[string]any { return map[string]any{"type": "string", "description": desc} }

var tools = []toolDef{
	{"claim_hostname", "Claim a hostname label in a zone. Returns the allocation or a conflict with suggestions.",
		obj(map[string]any{"zone": str("zone name, e.g. olsyn.test"), "label": str("label to claim, e.g. myapp"), "owner_ref": str("stable owner id, e.g. project name")}, "zone", "label")},
	{"claim_port", "Claim a sticky dev port. The same owner_ref always receives the same port — safe to write into config files.",
		obj(map[string]any{"owner_ref": str("stable owner id, e.g. 'olsyn-web/vite'"), "pool": str("pool name (default 'dev')")}, "owner_ref")},
	{"check_availability", "Check whether a label is claimable in a zone; returns reason and suggestions when not.",
		obj(map[string]any{"zone": str("zone name"), "label": str("candidate label")}, "zone", "label")},
	{"list_claims", "List allocations, optionally filtered by zone/owner_ref.",
		obj(map[string]any{"zone": str("filter by zone"), "owner_ref": str("filter by owner")})},
	{"release", "Release an allocation by id.",
		obj(map[string]any{"id": map[string]any{"type": "integer", "description": "allocation id"}}, "id")},
	{"describe_zone", "List zones and their configuration.", obj(map[string]any{})},
	{"start_service", "Start a supervised dev service by name (allocation FQDN).",
		obj(map[string]any{"name": str("process name, i.e. the FQDN")}, "name")},
	{"stop_service", "Stop a supervised dev service by name.",
		obj(map[string]any{"name": str("process name, i.e. the FQDN")}, "name")},
	{"tail_logs", "Tail captured logs of a supervised service.",
		obj(map[string]any{"name": str("process name"), "lines": map[string]any{"type": "integer", "description": "line count (default 100)"}}, "name")},
	{"rename_hostname", "Atomically rename an allocation's label. Availability and reserved names are enforced; id, owner, routes and history survive.",
		obj(map[string]any{"id": map[string]any{"type": "integer", "description": "allocation id"}, "label": str("new label")}, "id", "label")},
	{"registry_status", "Zones with their allocation counts — the machine's hostname state at a glance. Call this before inventing hostnames or ports.",
		obj(map[string]any{})},
}

// Run serves until EOF.
func (s *Server) Run(ctx context.Context) error {
	sc := bufio.NewScanner(s.In)
	sc.Buffer(make([]byte, 1024*1024), 8*1024*1024)
	enc := json.NewEncoder(s.Out)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var req rpcRequest
		if err := json.Unmarshal([]byte(line), &req); err != nil {
			continue
		}
		if req.ID == nil { // notification
			continue
		}
		resp := s.dispatch(ctx, &req)
		resp.JSONRPC = "2.0"
		resp.ID = req.ID
		if err := enc.Encode(resp); err != nil {
			return err
		}
	}
	return sc.Err()
}

func (s *Server) dispatch(ctx context.Context, req *rpcRequest) rpcResponse {
	switch req.Method {
	case "initialize":
		return rpcResponse{Result: map[string]any{
			"protocolVersion": "2024-11-05",
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "gerrymander", "version": "0.1.0"},
		}}
	case "tools/list":
		return rpcResponse{Result: map[string]any{"tools": tools}}
	case "tools/call":
		var params struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &params); err != nil {
			return rpcResponse{Error: &rpcError{Code: -32602, Message: err.Error()}}
		}
		text, isErr := s.callTool(ctx, params.Name, params.Arguments)
		return rpcResponse{Result: map[string]any{
			"content": []map[string]any{{"type": "text", "text": text}},
			"isError": isErr,
		}}
	case "ping":
		return rpcResponse{Result: map[string]any{}}
	default:
		return rpcResponse{Error: &rpcError{Code: -32601, Message: "method not found: " + req.Method}}
	}
}

func (s *Server) callTool(ctx context.Context, name string, argsRaw json.RawMessage) (string, bool) {
	var args map[string]any
	json.Unmarshal(argsRaw, &args)
	getS := func(k string) string {
		if v, ok := args[k].(string); ok {
			return v
		}
		return ""
	}
	getI := func(k string) int {
		if v, ok := args[k].(float64); ok {
			return int(v)
		}
		return 0
	}
	fail := func(err error) (string, bool) {
		var apiErr *client.APIError
		if ok := asAPIError(err, &apiErr); ok && len(apiErr.Suggestions) > 0 {
			return fmt.Sprintf("%s — suggestions: %s", apiErr.Error(), strings.Join(apiErr.Suggestions, ", ")), true
		}
		return err.Error(), true
	}
	okJSON := func(v any) (string, bool) {
		b, _ := json.MarshalIndent(v, "", "  ")
		return string(b), false
	}

	switch name {
	case "claim_hostname":
		var out map[string]any
		err := s.API.Do(ctx, "POST", "/v1/claims", map[string]any{
			"zone": getS("zone"), "label": getS("label"), "owner_ref": getS("owner_ref"),
		}, &out)
		if err != nil {
			return fail(err)
		}
		return okJSON(out)
	case "claim_port":
		pool := getS("pool")
		if pool == "" {
			pool = "dev"
		}
		var out map[string]any
		err := s.API.Do(ctx, "POST", "/v1/ports", map[string]any{"pool": pool, "owner_ref": getS("owner_ref")}, &out)
		if err != nil {
			return fail(err)
		}
		return okJSON(out)
	case "check_availability":
		var out map[string]any
		err := s.API.Do(ctx, "GET", "/v1/zones/"+getS("zone")+"/availability?label="+getS("label"), nil, &out)
		if err != nil {
			return fail(err)
		}
		return okJSON(out)
	case "list_claims":
		q := "?"
		if z := getS("zone"); z != "" {
			q += "zone=" + z + "&"
		}
		if o := getS("owner_ref"); o != "" {
			q += "owner_ref=" + o
		}
		var out map[string]any
		err := s.API.Do(ctx, "GET", "/v1/allocations"+strings.TrimSuffix(q, "?"), nil, &out)
		if err != nil {
			return fail(err)
		}
		return okJSON(out)
	case "release":
		err := s.API.Do(ctx, "DELETE", fmt.Sprintf("/v1/allocations/%d", getI("id")), nil, nil)
		if err != nil {
			return fail(err)
		}
		return "released", false
	case "rename_hostname":
		var out map[string]any
		err := s.API.Do(ctx, "POST", fmt.Sprintf("/v1/allocations/%d/rename", getI("id")), map[string]any{"label": getS("label")}, &out)
		if err != nil {
			return fail(err)
		}
		return okJSON(out)
	case "registry_status":
		var zones map[string]any
		if err := s.API.Do(ctx, "GET", "/v1/zones", nil, &zones); err != nil {
			return fail(err)
		}
		var allocs struct {
			Allocations []map[string]any `json:"allocations"`
		}
		if err := s.API.Do(ctx, "GET", "/v1/allocations", nil, &allocs); err != nil {
			return fail(err)
		}
		perZone := map[string]int{}
		for _, a := range allocs.Allocations {
			if z, ok := a["zone"].(string); ok {
				perZone[z]++
			}
		}
		return okJSON(map[string]any{"zones": zones["zones"], "allocations_per_zone": perZone, "total_allocations": len(allocs.Allocations)})
	case "describe_zone":
		var out map[string]any
		err := s.API.Do(ctx, "GET", "/v1/zones", nil, &out)
		if err != nil {
			return fail(err)
		}
		return okJSON(out)
	case "start_service":
		err := s.API.Do(ctx, "POST", "/v1/processes/"+getS("name")+"/start", nil, nil)
		if err != nil {
			return fail(err)
		}
		return "started", false
	case "stop_service":
		err := s.API.Do(ctx, "POST", "/v1/processes/"+getS("name")+"/stop", nil, nil)
		if err != nil {
			return fail(err)
		}
		return "stopped", false
	case "tail_logs":
		n := getI("lines")
		if n <= 0 {
			n = 100
		}
		var out struct {
			Lines []string `json:"lines"`
		}
		err := s.API.Do(ctx, "GET", fmt.Sprintf("/v1/processes/%s/logs?lines=%d", getS("name"), n), nil, &out)
		if err != nil {
			return fail(err)
		}
		return strings.Join(out.Lines, "\n"), false
	default:
		return "unknown tool: " + name, true
	}
}

func asAPIError(err error, target **client.APIError) bool {
	for err != nil {
		if e, ok := err.(*client.APIError); ok {
			*target = e
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}
