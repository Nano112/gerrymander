package rexlink

import (
	"crypto/tls"
	"net/http/httptest"
	"testing"
)

func TestBrowserURL(t *testing.T) {
	hosts := Hosts{"mini": {RemoteZone: "olsyn.test", LocalZone: "olsyn.dev"}}
	got, err := hosts.BrowserURL("gerry://mini/drive.olsyn.test/files/a%2Fb?q=one%20two#view")
	if err != nil || got.String() != "https://drive.olsyn.dev/files/a%2Fb?q=one%20two#view" {
		t.Fatalf("%v %v", got, err)
	}
	for _, raw := range []string{"https://mini/drive.olsyn.test", "gerry://other/drive.olsyn.test", "gerry://mini/drive.olsyn.test.example", "gerry://mini/badolsyn.test", "gerry://mini/%2Fother", "gerry://user@mini/olsyn.test"} {
		if _, err := hosts.BrowserURL(raw); err == nil {
			t.Errorf("accepted %q", raw)
		}
	}
}

func TestBridgeRequiresLocalTLS(t *testing.T) {
	rt := Router{bridges: []*bridge{{Host: Host{LocalZone: "olsyn.dev", RemoteZone: "olsyn.test"}}}}
	for _, tc := range []struct{ peer, sni string }{{"192.0.2.1:5000", "drive.olsyn.dev"}, {"127.0.0.1:5000", "other.example"}} {
		r := httptest.NewRequest("GET", "https://drive.olsyn.dev/", nil)
		r.RemoteAddr = tc.peer
		r.TLS = &tls.ConnectionState{ServerName: tc.sni}
		w := httptest.NewRecorder()
		if !rt.Serve(w, r, "drive.olsyn.dev") || w.Code != 403 {
			t.Fatal(w.Code)
		}
	}
}

func TestTerminalLink(t *testing.T) {
	if got := TerminalLink("mini", "drive.olsyn.test"); got != "\x1b]8;;gerry://mini/drive.olsyn.test/\x1b\\https://drive.olsyn.test\x1b]8;;\x1b\\" {
		t.Fatalf("%q", got)
	}
	if got := TerminalLink("", "drive.olsyn.test"); got != "https://drive.olsyn.test" {
		t.Fatal(got)
	}
}
