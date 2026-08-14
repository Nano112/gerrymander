package supervise

import (
	"context"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Nano112/gerrymander/internal/core"
	"github.com/Nano112/gerrymander/internal/service"
	"github.com/Nano112/gerrymander/internal/store"
)

func testManager(t *testing.T) *Manager {
	t.Helper()
	st, err := store.Open("sqlite:" + filepath.Join(t.TempDir(), "sup.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ports := service.NewPorts(st)
	if err := ports.EnsureDefaultPool(context.Background()); err != nil {
		t.Fatal(err)
	}
	m := NewManager(ports)
	m.HealthTimeout = 10 * time.Second
	t.Cleanup(m.StopAll)
	return m
}

// A tiny python http server that binds ${PORT} after a short delay.
func slowServer(delay string) *core.SupervisedBackend {
	return &core.SupervisedBackend{
		Cmd: fmt.Sprintf(`sleep %s; exec python3 -m http.server ${PORT} --bind 127.0.0.1`, delay),
		Dir: "/tmp",
	}
}

func TestColdStartHoldsRequest(t *testing.T) {
	m := testManager(t)
	alloc := core.Allocation{FQDN: "vite.olsyn.test"}
	start := time.Now()
	addr, err := m.Ensure(context.Background(), alloc, slowServer("0.5"))
	if err != nil {
		t.Fatal(err)
	}
	if time.Since(start) < 400*time.Millisecond {
		t.Error("Ensure returned before the process could have booted")
	}
	resp, err := http.Get("http://" + addr + "/")
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("proxied backend not serving: %v", err)
	}
	resp.Body.Close()

	// Second Ensure is instant (running).
	start = time.Now()
	addr2, err := m.Ensure(context.Background(), alloc, slowServer("0.5"))
	if err != nil || addr2 != addr {
		t.Fatalf("warm ensure: %v %s vs %s", err, addr2, addr)
	}
	if time.Since(start) > 200*time.Millisecond {
		t.Error("warm Ensure was slow — did it restart?")
	}
}

func TestIdleSleepAndRestart(t *testing.T) {
	m := testManager(t)
	alloc := core.Allocation{FQDN: "sleepy.olsyn.test"}
	spec := slowServer("0")
	spec.IdleTimeout = core.Duration(300 * time.Millisecond)
	addr, err := m.Ensure(context.Background(), alloc, spec)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(600 * time.Millisecond)
	m.sweep()
	time.Sleep(300 * time.Millisecond) // let SIGTERM land
	list := m.List()
	if len(list) != 1 || list[0]["state"] != "stopped" {
		t.Fatalf("idle proc should be stopped: %+v", list)
	}
	// Next request restarts it on the SAME sticky port.
	addr2, err := m.Ensure(context.Background(), alloc, spec)
	if err != nil {
		t.Fatal(err)
	}
	if addr2 != addr {
		t.Fatalf("sticky port lost across idle sleep: %s vs %s", addr2, addr)
	}
}

func TestCrashLoopParks(t *testing.T) {
	m := testManager(t)
	m.HealthTimeout = 700 * time.Millisecond
	alloc := core.Allocation{FQDN: "crashy.olsyn.test"}
	spec := &core.SupervisedBackend{Cmd: "exit 1", Dir: "/tmp"}
	for i := 0; i < crashThreshold; i++ {
		if _, err := m.Ensure(context.Background(), alloc, spec); err == nil {
			t.Fatal("crashing process reported healthy")
		}
	}
	if _, err := m.Ensure(context.Background(), alloc, spec); err == nil || !strings.Contains(err.Error(), "parked") {
		t.Fatalf("want parked error, got %v", err)
	}
}

func TestLogsCaptured(t *testing.T) {
	m := testManager(t)
	m.HealthTimeout = 700 * time.Millisecond
	alloc := core.Allocation{FQDN: "logger.olsyn.test"}
	m.Ensure(context.Background(), alloc, &core.SupervisedBackend{Cmd: "echo hello-from-proc; exit 1", Dir: "/tmp"})
	time.Sleep(200 * time.Millisecond)
	lines, err := m.Logs("logger.olsyn.test", 10)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, l := range lines {
		if strings.Contains(l, "hello-from-proc") {
			found = true
		}
	}
	if !found {
		t.Fatalf("stdout not captured: %v", lines)
	}
}

func TestRingBuffer(t *testing.T) {
	r := newRing(3)
	for i := 0; i < 10; i++ {
		fmt.Fprintf(r, "line-%d\n", i)
	}
	tail := r.Tail(5)
	if len(tail) != 3 || tail[2] != "line-9" || tail[0] != "line-7" {
		t.Fatalf("ring: %v", tail)
	}
}
