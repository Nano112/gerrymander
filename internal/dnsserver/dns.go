// Package dnsserver answers wildcard queries for dev zones on loopback,
// replacing a hand-configured dnsmasq for fresh installs. Machines that
// already run dnsmasq with address=/.test/127.0.0.1 do not need it.
package dnsserver

import (
	"context"
	"log/slog"
	"net"
	"strings"

	"github.com/miekg/dns"
)

// Server answers A/AAAA for configured zones with 127.0.0.1/::1.
type Server struct {
	zones []string // e.g. ["test"] or ["olsyn.test"]
	addr  string
	log   *slog.Logger
	srvs  []*dns.Server
}

// New builds a server for the given zones (TLDs or full zones) on addr
// (e.g. "127.0.0.1:5353").
func New(zones []string, addr string, log *slog.Logger) *Server {
	if log == nil {
		log = slog.Default()
	}
	norm := make([]string, 0, len(zones))
	for _, z := range zones {
		norm = append(norm, strings.Trim(strings.ToLower(z), "."))
	}
	return &Server{zones: norm, addr: addr, log: log}
}

func (s *Server) matches(name string) bool {
	q := strings.Trim(strings.ToLower(name), ".")
	for _, z := range s.zones {
		if q == z || strings.HasSuffix(q, "."+z) {
			return true
		}
	}
	return false
}

func (s *Server) handle(w dns.ResponseWriter, r *dns.Msg) {
	m := new(dns.Msg)
	m.SetReply(r)
	m.Authoritative = true
	for _, q := range r.Question {
		if !s.matches(q.Name) {
			m.SetRcode(r, dns.RcodeNameError)
			continue
		}
		switch q.Qtype {
		case dns.TypeA:
			m.Answer = append(m.Answer, &dns.A{
				Hdr: dns.RR_Header{Name: q.Name, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 10},
				A:   net.ParseIP("127.0.0.1"),
			})
		case dns.TypeAAAA:
			m.Answer = append(m.Answer, &dns.AAAA{
				Hdr:  dns.RR_Header{Name: q.Name, Rrtype: dns.TypeAAAA, Class: dns.ClassINET, Ttl: 10},
				AAAA: net.ParseIP("::1"),
			})
		}
	}
	w.WriteMsg(m)
}

// Run serves UDP+TCP until ctx is done.
func (s *Server) Run(ctx context.Context) error {
	mux := dns.NewServeMux()
	mux.HandleFunc(".", s.handle)
	s.srvs = []*dns.Server{
		{Addr: s.addr, Net: "udp", Handler: mux},
		{Addr: s.addr, Net: "tcp", Handler: mux},
	}
	errc := make(chan error, 2)
	for _, srv := range s.srvs {
		srv := srv
		go func() { errc <- srv.ListenAndServe() }()
	}
	select {
	case <-ctx.Done():
		for _, srv := range s.srvs {
			srv.Shutdown()
		}
		return nil
	case err := <-errc:
		return err
	}
}
