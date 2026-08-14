// Package supervise runs dev processes on demand: boot on first request,
// health-gate, sleep when idle, park on crash loops. It sits strictly behind
// the proxy.Starter interface — the core never depends on it.
package supervise

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/Nano112/gerrymander/internal/core"
	"github.com/Nano112/gerrymander/internal/service"
)

const (
	defaultIdleTimeout   = 30 * time.Minute
	defaultHealthTimeout = 30 * time.Second
	crashWindow          = time.Minute
	crashThreshold       = 3
	parkBackoff          = 30 * time.Second
	logRingSize          = 2000
)

// procState is a supervised process's lifecycle.
type procState string

const (
	stateStopped  procState = "stopped"
	stateStarting procState = "starting"
	stateRunning  procState = "running"
	stateParked   procState = "parked" // crash-looped; wait for backoff
)

type process struct {
	mu       sync.Mutex
	name     string // allocation FQDN
	spec     core.SupervisedBackend
	state    procState
	cmd      *exec.Cmd
	port     int
	lastReq  time.Time
	failures []time.Time
	parkedAt time.Time
	logs     *ring
	ready    chan struct{} // closed when running
}

// Manager supervises processes keyed by allocation FQDN.
type Manager struct {
	mu    sync.Mutex
	procs map[string]*process

	Ports *service.Ports
	Log   *slog.Logger
	// HealthTimeout overrides per-spec health timeouts when they are unset.
	HealthTimeout time.Duration
	// IdleSweep interval; also the test hook granularity.
	IdleSweep time.Duration
	now       func() time.Time
}

// NewManager wires a supervisor.
func NewManager(ports *service.Ports) *Manager {
	return &Manager{
		procs:         map[string]*process{},
		Ports:         ports,
		Log:           slog.Default(),
		HealthTimeout: defaultHealthTimeout,
		IdleSweep:     30 * time.Second,
		now:           time.Now,
	}
}

// Ensure implements proxy.Starter: returns a live address for the backend,
// booting it if necessary. Blocks until healthy or errors.
func (m *Manager) Ensure(ctx context.Context, alloc core.Allocation, spec *core.SupervisedBackend) (string, error) {
	p := m.proc(alloc.FQDN, *spec)

	p.mu.Lock()
	p.lastReq = m.now()
	switch p.state {
	case stateRunning:
		addr := fmt.Sprintf("127.0.0.1:%d", p.port)
		p.mu.Unlock()
		return addr, nil
	case stateParked:
		if m.now().Sub(p.parkedAt) < parkBackoff {
			p.mu.Unlock()
			return "", fmt.Errorf("process crash-looped; parked for %s (see /v1/processes/%s/logs)", parkBackoff, alloc.FQDN)
		}
		p.state = stateStopped
		p.failures = nil
	case stateStarting:
		ready := p.ready
		p.mu.Unlock()
		return m.await(ctx, p, ready)
	}
	// stateStopped → start
	p.state = stateStarting
	p.ready = make(chan struct{})
	ready := p.ready
	p.mu.Unlock()

	if err := m.start(ctx, alloc, p); err != nil {
		p.mu.Lock()
		p.state = stateStopped
		p.recordFailure(m.now())
		close(ready)
		p.mu.Unlock()
		return "", err
	}
	return m.await(ctx, p, ready)
}

func (m *Manager) await(ctx context.Context, p *process, ready chan struct{}) (string, error) {
	select {
	case <-ready:
	case <-ctx.Done():
		return "", ctx.Err()
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.state != stateRunning {
		return "", fmt.Errorf("process failed to become healthy (state=%s)", p.state)
	}
	return fmt.Sprintf("127.0.0.1:%d", p.port), nil
}

func (m *Manager) proc(name string, spec core.SupervisedBackend) *process {
	m.mu.Lock()
	defer m.mu.Unlock()
	if p, ok := m.procs[name]; ok {
		return p
	}
	p := &process{name: name, spec: spec, state: stateStopped, logs: newRing(logRingSize)}
	m.procs[name] = p
	return p
}

// start launches the command and health-polls in the background; it closes
// p.ready when the process is routable (or failed).
func (m *Manager) start(ctx context.Context, alloc core.Allocation, p *process) error {
	port := p.port
	if port == 0 {
		pool := p.spec.PortPool
		if pool == "" {
			pool = "dev"
		}
		pa, err := m.Ports.Claim(context.WithoutCancel(ctx), pool, alloc.Project, "supervised:"+alloc.FQDN)
		if err != nil {
			return fmt.Errorf("port claim: %w", err)
		}
		port = pa.Value
	}

	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/sh"
	}
	cmdline := strings.ReplaceAll(p.spec.Cmd, "${PORT}", strconv.Itoa(port))
	cmd := exec.Command(shell, "-c", cmdline)
	cmd.Dir = p.spec.Dir
	cmd.Env = os.Environ()
	cmd.Env = append(cmd.Env, "PORT="+strconv.Itoa(port))
	for k, v := range p.spec.Env {
		cmd.Env = append(cmd.Env, k+"="+strings.ReplaceAll(v, "${PORT}", strconv.Itoa(port)))
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true} // kill the whole group
	cmd.Stdout = p.logs
	cmd.Stderr = p.logs
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start %q: %w", cmdline, err)
	}
	p.mu.Lock()
	p.cmd = cmd
	p.port = port
	p.mu.Unlock()
	m.Log.Info("supervised start", "name", p.name, "pid", cmd.Process.Pid, "port", port)

	go m.healthGate(p)
	go func() {
		err := cmd.Wait()
		p.mu.Lock()
		defer p.mu.Unlock()
		wasRunning := p.state == stateRunning
		starting := p.state == stateStarting
		p.cmd = nil
		if starting {
			p.state = stateStopped // healthGate's close still pending; it checks state
		} else {
			p.state = stateStopped
		}
		if wasRunning || starting {
			// Unexpected exit (idle stops move state to stopped first).
			p.recordFailure(m.now())
			m.Log.Warn("supervised exit", "name", p.name, "err", err)
			if len(p.failures) >= crashThreshold {
				p.state = stateParked
				p.parkedAt = m.now()
				m.Log.Error("supervised parked after crash loop", "name", p.name)
			}
		}
	}()
	return nil
}

// healthGate polls until healthy, then flips to running and closes ready.
func (m *Manager) healthGate(p *process) {
	p.mu.Lock()
	port := p.port
	spec := p.spec
	ready := p.ready
	p.mu.Unlock()

	timeout := m.HealthTimeout
	if spec.Health != nil && spec.Health.Timeout.Std() > 0 {
		timeout = spec.Health.Timeout.Std()
	}
	path := "/"
	if spec.Health != nil && spec.Health.Path != "" {
		path = spec.Health.Path
	}
	deadline := m.now().Add(timeout)
	for m.now().Before(deadline) {
		p.mu.Lock()
		if p.state != stateStarting { // died while we polled
			p.mu.Unlock()
			close(ready)
			return
		}
		p.mu.Unlock()
		if healthy(port, path) {
			p.mu.Lock()
			if p.state == stateStarting {
				p.state = stateRunning
			}
			p.mu.Unlock()
			close(ready)
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	// Timed out: kill and record.
	p.mu.Lock()
	p.state = stateStopped
	p.recordFailure(m.now())
	cmd := p.cmd
	p.mu.Unlock()
	if cmd != nil && cmd.Process != nil {
		syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	close(ready)
}

func healthy(port int, path string) bool {
	// TCP-connect first; HTTP path check when it accepts.
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 500*time.Millisecond)
	if err != nil {
		return false
	}
	conn.Close()
	client := http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d%s", port, path))
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode < 500
}

func (p *process) recordFailure(now time.Time) {
	keep := p.failures[:0]
	for _, f := range p.failures {
		if now.Sub(f) < crashWindow {
			keep = append(keep, f)
		}
	}
	p.failures = append(keep, now)
}

// RunIdleSweeper stops idle processes until ctx is done.
func (m *Manager) RunIdleSweeper(ctx context.Context) {
	t := time.NewTicker(m.IdleSweep)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			m.sweep()
		}
	}
}

func (m *Manager) sweep() {
	m.mu.Lock()
	procs := make([]*process, 0, len(m.procs))
	for _, p := range m.procs {
		procs = append(procs, p)
	}
	m.mu.Unlock()
	for _, p := range procs {
		p.mu.Lock()
		idle := p.spec.IdleTimeout.Std()
		if idle <= 0 {
			idle = defaultIdleTimeout
		}
		if p.state == stateRunning && m.now().Sub(p.lastReq) > idle {
			m.Log.Info("supervised idle stop", "name", p.name)
			p.stopLocked()
		}
		p.mu.Unlock()
	}
}

func (p *process) stopLocked() {
	if p.cmd != nil && p.cmd.Process != nil {
		p.state = stateStopped // set before kill so Wait() doesn't count it as a crash
		syscall.Kill(-p.cmd.Process.Pid, syscall.SIGTERM)
	} else {
		p.state = stateStopped
	}
}

// --- api.ProcessController implementation ---

// List reports all known processes.
func (m *Manager) List() []map[string]any {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []map[string]any{}
	for _, p := range m.procs {
		p.mu.Lock()
		e := map[string]any{"name": p.name, "state": string(p.state), "port": p.port}
		if p.cmd != nil && p.cmd.Process != nil {
			e["pid"] = p.cmd.Process.Pid
		}
		p.mu.Unlock()
		out = append(out, e)
	}
	return out
}

// Start boots a known process by name (no-op if running).
func (m *Manager) Start(name string) error {
	m.mu.Lock()
	p, ok := m.procs[name]
	m.mu.Unlock()
	if !ok {
		return fmt.Errorf("unknown process %q", name)
	}
	_, err := m.Ensure(context.Background(), core.Allocation{FQDN: p.name}, &p.spec)
	return err
}

// Stop terminates a process by name.
func (m *Manager) Stop(name string) error {
	m.mu.Lock()
	p, ok := m.procs[name]
	m.mu.Unlock()
	if !ok {
		return fmt.Errorf("unknown process %q", name)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.stopLocked()
	return nil
}

// Logs returns the last n captured lines.
func (m *Manager) Logs(name string, n int) ([]string, error) {
	m.mu.Lock()
	p, ok := m.procs[name]
	m.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("unknown process %q", name)
	}
	return p.logs.Tail(n), nil
}

// StopAll terminates everything (shutdown path).
func (m *Manager) StopAll() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, p := range m.procs {
		p.mu.Lock()
		p.stopLocked()
		p.mu.Unlock()
	}
}

// --- log ring buffer ---

type ring struct {
	mu    sync.Mutex
	lines []string
	max   int
	part  []byte
}

func newRing(max int) *ring { return &ring{max: max} }

func (r *ring) Write(b []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.part = append(r.part, b...)
	for {
		i := indexByte(r.part, '\n')
		if i < 0 {
			break
		}
		r.lines = append(r.lines, string(r.part[:i]))
		r.part = r.part[i+1:]
		if len(r.lines) > r.max {
			r.lines = r.lines[len(r.lines)-r.max:]
		}
	}
	return len(b), nil
}

func (r *ring) Tail(n int) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if n >= len(r.lines) {
		return append([]string{}, r.lines...)
	}
	return append([]string{}, r.lines[len(r.lines)-n:]...)
}

func indexByte(b []byte, c byte) int {
	for i, x := range b {
		if x == c {
			return i
		}
	}
	return -1
}
