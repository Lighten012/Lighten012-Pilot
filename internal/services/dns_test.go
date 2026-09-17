package services

import (
	"context"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/miekg/dns"
)

func testConfig() Config {
	c := Default()
	c.Interface = "lan0"
	c.Address = "192.168.60.1/24"
	c.DNS.Enabled = true
	c.DNS.Records = []Record{{"lighten012.home.", "A", "192.168.1.1", 60}, {"v6.home.", "AAAA", "fd00::1", 60}}
	c.DHCP = DHCPConfig{true, "192.168.60.100", "192.168.60.200", 720}
	return c
}
func TestLocalResolution(t *testing.T) {
	r := NewResolver(testConfig())
	r.upstream = "127.0.0.1:1"
	for _, tc := range []struct {
		name   string
		typ    uint16
		count  int
		answer string
	}{{"LiGhTeN012.HOME.", dns.TypeA, 1, "192.168.1.1"}, {"lighten012.home.", dns.TypeAAAA, 0, ""}, {"lighten012.home.", dns.TypeHTTPS, 0, ""}, {"v6.home.", dns.TypeAAAA, 1, "fd00::1"}} {
		q := new(dns.Msg)
		q.SetQuestion(tc.name, tc.typ)
		q.AuthenticatedData = true
		m := r.Resolve(context.Background(), q)
		if m.Rcode != 0 || len(m.Answer) != tc.count || m.Id != q.Id || !m.Authoritative || m.AuthenticatedData {
			t.Fatalf("unexpected local response: %s", m)
		}
		if tc.count > 0 && !strings.Contains(m.Answer[0].String(), tc.answer) {
			t.Fatal(m.Answer)
		}
	}
	if r.Stats().Forwarded != 0 {
		t.Fatal("local query leaked upstream")
	}
}
func mockUpstream(t *testing.T, handler dns.HandlerFunc) string {
	t.Helper()
	tcp, e := net.Listen("tcp4", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	udp, e := net.ListenPacket("udp4", tcp.Addr().String())
	if e != nil {
		tcp.Close()
		t.Fatal(e)
	}
	for _, s := range []*dns.Server{{Listener: tcp, Handler: handler}, {PacketConn: udp, Handler: handler}} {
		started := make(chan struct{})
		s.NotifyStartedFunc = func() { close(started) }
		go s.ActivateAndServe()
		<-started
		t.Cleanup(func() { s.Shutdown() })
	}
	return tcp.Addr().String()
}
func TestForwardTCPFallback(t *testing.T) {
	var udp, tcp atomic.Int32
	addr := mockUpstream(t, func(w dns.ResponseWriter, q *dns.Msg) {
		m := new(dns.Msg)
		m.SetReply(q)
		if _, ok := w.RemoteAddr().(*net.UDPAddr); ok {
			udp.Add(1)
			m.Truncated = true
		} else {
			tcp.Add(1)
			m.Answer = []dns.RR{&dns.A{Hdr: dns.RR_Header{Name: q.Question[0].Name, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 30}, A: net.ParseIP("203.0.113.10")}}
		}
		w.WriteMsg(m)
	})
	r := NewResolver(testConfig())
	r.upstream = addr
	q := new(dns.Msg)
	q.SetQuestion("forward.example.", dns.TypeA)
	m := r.Resolve(context.Background(), q)
	if m.Id != q.Id || m.Rcode != 0 || len(m.Answer) != 1 || udp.Load() != 1 || tcp.Load() != 1 {
		t.Fatalf("fallback failed: %s", m)
	}
}
func TestForwardFailureAndQuestionMismatch(t *testing.T) {
	r := NewResolver(testConfig())
	r.upstream = mockUpstream(t, func(w dns.ResponseWriter, q *dns.Msg) {
		m := new(dns.Msg)
		m.SetReply(q)
		m.Question[0].Name = "wrong.example."
		w.WriteMsg(m)
	})
	q := new(dns.Msg)
	q.SetQuestion("forward.example.", dns.TypeA)
	if m := r.Resolve(context.Background(), q); m.Rcode != dns.RcodeServerFailure {
		t.Fatal(m)
	}
	r.upstream = "127.0.0.1:1"
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if m := r.Resolve(ctx, q); m.Rcode != dns.RcodeServerFailure {
		t.Fatal(m)
	}
	if r.Stats().Failed != 2 {
		t.Fatal(r.Stats())
	}
}
func TestBadQueriesAndLocalExactMatch(t *testing.T) {
	r := NewResolver(testConfig())
	r.upstream = "127.0.0.1:1"
	q := new(dns.Msg)
	q.SetQuestion("lighten012.home.", dns.TypeAXFR)
	if m := r.Resolve(context.Background(), q); m.Rcode != dns.RcodeRefused {
		t.Fatal(m)
	}
	q.SetQuestion("child.lighten012.home.", dns.TypeA)
	r.Resolve(context.Background(), q)
	if r.Stats().Local != 0 || r.Stats().Forwarded != 1 {
		t.Fatal("unexpected wildcard matching")
	}
	q.Question = append(q.Question, q.Question[0])
	if m := r.Resolve(context.Background(), q); m.Rcode != dns.RcodeFormatError {
		t.Fatal(m)
	}
}
