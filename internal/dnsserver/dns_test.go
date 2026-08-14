package dnsserver

import (
	"context"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/miekg/dns"
)

func TestWildcardAnswers(t *testing.T) {
	// grab a free UDP port
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := pc.LocalAddr().String()
	pc.Close()

	s := New([]string{"olsyn.test"}, addr, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.Run(ctx)
	time.Sleep(200 * time.Millisecond)

	query := func(name string, qtype uint16) *dns.Msg {
		m := new(dns.Msg)
		m.SetQuestion(dns.Fqdn(name), qtype)
		c := new(dns.Client)
		resp, _, err := c.Exchange(m, addr)
		if err != nil {
			t.Fatalf("query %s: %v", name, err)
		}
		return resp
	}

	r := query("anything.olsyn.test", dns.TypeA)
	if len(r.Answer) != 1 {
		t.Fatalf("wildcard A: %+v", r)
	}
	if a, ok := r.Answer[0].(*dns.A); !ok || a.A.String() != "127.0.0.1" {
		t.Fatalf("A record: %+v", r.Answer[0])
	}
	r = query("deep.nested.olsyn.test", dns.TypeAAAA)
	if len(r.Answer) != 1 {
		t.Fatalf("AAAA: %+v", r)
	}
	r = query("olsyn.test", dns.TypeA)
	if len(r.Answer) != 1 {
		t.Fatal("apex should answer")
	}
	r = query("example.com", dns.TypeA)
	if r.Rcode != dns.RcodeNameError {
		t.Fatalf("out-of-zone should NXDOMAIN, got %s", fmt.Sprint(r.Rcode))
	}
}
