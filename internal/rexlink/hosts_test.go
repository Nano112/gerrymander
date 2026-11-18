package rexlink

import (
	"crypto/tls"
	"net/http/httptest"
	"testing"
)

func TestBrowserURL(t *testing.T) {
	hosts := Hosts{"mini": {RemoteZone: "example.test", LocalZone: "example.dev"}}
	got, err := hosts.BrowserURL("gerry://mini/drive.example.test/files/a%2Fb?q=one%20two#view")
	if err != nil || got.String() != "https://drive.example.dev/files/a%2Fb?q=one%20two#view" {
		t.Fatalf("%v %v", got, err)
	}
	for _, raw := range []string{"https://mini/drive.example.test", "gerry://other/drive.example.test", "gerry://mini/drive.example.test.example", "gerry://mini/badexample.test", "gerry://mini/%2Fother", "gerry://user@mini/example.test"} {
		if _, err := hosts.BrowserURL(raw); err == nil {
			t.Errorf("accepted %q", raw)
		}
	}
}

func TestBridgeRequiresLocalTLS(t *testing.T) {
	rt := Router{bridges: []*bridge{{Host: Host{LocalZone: "example.dev", RemoteZone: "example.test"}}}}
	for _, tc := range []struct{ peer, sni string }{{"192.0.2.1:5000", "drive.example.dev"}, {"127.0.0.1:5000", "other.example"}} {
		r := httptest.NewRequest("GET", "https://drive.example.dev/", nil)
		r.RemoteAddr = tc.peer
		r.TLS = &tls.ConnectionState{ServerName: tc.sni}
		w := httptest.NewRecorder()
		if !rt.Serve(w, r, "drive.example.dev") || w.Code != 403 {
			t.Fatal(w.Code)
		}
	}
}

func TestTerminalLink(t *testing.T) {
	if got := TerminalLink("mini", "drive.example.test"); got != "\x1b]8;;gerry://mini/drive.example.test/\x1b\\https://drive.example.test\x1b]8;;\x1b\\" {
		t.Fatalf("%q", got)
	}
	if got := TerminalLink("", "drive.example.test"); got != "https://drive.example.test" {
		t.Fatal(got)
	}
}
