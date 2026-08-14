package main

import (
	"context"
	"testing"
)

func TestParseLsof(t *testing.T) {
	sample := `COMMAND   PID     USER   FD   TYPE             DEVICE SIZE/OFF NODE NAME
node    54321 harrison   23u  IPv4 0xdeadbeef      0t0  TCP 127.0.0.1:5173 (LISTEN)
node    54321 harrison   24u  IPv6 0xdeadbeef      0t0  TCP [::1]:5173 (LISTEN)
postgres  777 harrison    7u  IPv4 0xdeadbeef      0t0  TCP *:54320 (LISTEN)
`
	ls := parseLsof(sample)
	if len(ls) != 2 {
		t.Fatalf("want 2 (dual-stack deduped), got %d: %+v", len(ls), ls)
	}
	if ls[0].Port != 5173 || ls[0].PID != 54321 || ls[0].Command != "node" {
		t.Fatalf("first: %+v", ls[0])
	}
	if ls[1].Port != 54320 || ls[1].Command != "postgres" {
		t.Fatalf("second: %+v", ls[1])
	}
}

func TestScanListenersLive(t *testing.T) {
	ls, err := ScanListeners(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// This machine always has something listening (docker, gerry, etc).
	if len(ls) == 0 {
		t.Skip("no listeners visible — CI sandbox?")
	}
	for _, l := range ls {
		if l.Port <= 0 || l.PID <= 0 {
			t.Fatalf("bad row: %+v", l)
		}
	}
}

func TestKillRefusesLowPids(t *testing.T) {
	if err := KillProcess(1, false); err == nil {
		t.Fatal("must refuse pid 1")
	}
	if err := KillProcess(0, true); err == nil {
		t.Fatal("must refuse pid 0")
	}
}
