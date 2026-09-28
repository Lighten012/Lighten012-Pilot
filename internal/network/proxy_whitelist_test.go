package network

import (
	"context"
	"net"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/Lighten012/Lighten012-Pilot/internal/dns"
	"github.com/miekg/dns"
	"gopkg.in/yaml.v3"
)

func TestProxyWhitelistRulesUseDirectFallback(t *testing.T) {
	config, err := normalizeProxyWhitelist(proxyWhitelistConfig{Domains: []string{" Google.com ", "google.com", "example.org"}, Group: "PROXY"})
	if err != nil {
		t.Fatal(err)
	}
	if len(config.Domains) != 2 || config.Domains[0] != "google.com" {
		t.Fatalf("unexpected domains: %+v", config.Domains)
	}
	base := []byte("external-controller: 127.0.0.1:9090\nallow-lan: false\nproxy-groups:\n  - name: PROXY\n    type: select\n    proxies: [DIRECT]\nrules:\n  - MATCH,PROXY\n")
	result, err := applyWhitelistRules(base, config)
	if err != nil {
		t.Fatal(err)
	}
	for _, rule := range []string{"DOMAIN-SUFFIX,google.com,PROXY", "DOMAIN-SUFFIX,example.org,PROXY", "MATCH,DIRECT"} {
		if !strings.Contains(string(result), rule) {
			t.Fatalf("missing %s", rule)
		}
	}
	var parsed map[string]any
	if err := yaml.Unmarshal(result, &parsed); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(result), "pilot-full-device") || !strings.Contains(string(result), "port: 7894") {
		t.Fatal("missing independent full-device listener")
	}
	for _, listener := range []string{"pilot-full-device-udp", "pilot-whitelist-udp", "port: 7895", "port: 7896"} {
		if !strings.Contains(string(result), listener) {
			t.Fatalf("missing UDP listener %s", listener)
		}
	}
	if _, err := applyWhitelistRules(base, proxyWhitelistConfig{Domains: []string{"google.com"}, Group: "missing"}); err == nil {
		t.Fatal("unknown proxy group accepted")
	}
}

func TestFullDevicePrecedesWhitelist(t *testing.T) {
	var rules []string
	p := &proxyWhitelist{lan: "eth1", subnet: "10.0.0.0/24", fullDevices: map[string]bool{"bc:24:11:41:5a:aa": true}, iptRun: func(args ...string) error {
		rules = append(rules, strings.Join(args, " "))
		return nil
	}}
	if err := p.applyDeviceRules(); err != nil {
		t.Fatal(err)
	}
	full, whitelist, fullUDP, whitelistUDP := -1, -1, -1, -1
	for i, rule := range rules {
		if strings.Contains(rule, "--to-ports 7894") {
			full = i
		}
		if strings.Contains(rule, "--to-ports 7893") {
			whitelist = i
		}
		if strings.Contains(rule, "--on-port 7895") {
			fullUDP = i
		}
		if strings.Contains(rule, "--on-port 7896") {
			whitelistUDP = i
		}
	}
	if full < 0 || whitelist <= full || fullUDP < 0 || whitelistUDP <= fullUDP {
		t.Fatalf("full-device rule must precede whitelist: %v", rules)
	}
}

func TestWhitelistDNSUsesMihomo(t *testing.T) {
	conn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := &dns.Server{PacketConn: conn, Handler: dns.HandlerFunc(func(w dns.ResponseWriter, q *dns.Msg) {
		answer := new(dns.Msg)
		answer.SetReply(q)
		answer.Answer = []dns.RR{&dns.A{Hdr: dns.RR_Header{Name: q.Question[0].Name, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 60}, A: net.ParseIP("198.18.0.2").To4()}}
		_ = w.WriteMsg(answer)
	})}
	go func() { _ = server.ActivateAndServe() }()
	defer server.Shutdown()
	r := dnsservice.NewResolver(dnsservice.Config{Upstream: "119.29.29.29", Records: []dnsservice.Record{{Name: "google.com", Type: "A", Value: "192.0.2.1", TTL: 60}}})
	r.SetProxy(&proxyWhitelist{config: proxyWhitelistConfig{Domains: []string{"google.com"}}})
	r.SetMihomoDNSAddr(conn.LocalAddr().String())
	query := new(dns.Msg)
	query.SetQuestion("google.com.", dns.TypeA)
	answer := r.Resolve(context.Background(), query, "udp")
	if len(answer.Answer) != 1 || answer.Answer[0].(*dns.A).A.String() != "198.18.0.2" {
		t.Fatalf("unexpected answer: %+v", answer)
	}
	if stats := r.Stats(); stats.Mihomo != 1 || stats.Local != 0 {
		t.Fatalf("unexpected stats: %+v", stats)
	}
}

func TestWhitelistTracksMihomoFakeIP(t *testing.T) {
	if ip := netip.MustParseAddr("198.18.0.2"); !ip.IsGlobalUnicast() || ip.IsPrivate() {
		t.Fatal("fake IP cannot be tracked")
	}
	called := false
	p := &proxyWhitelist{active: true, config: proxyWhitelistConfig{Domains: []string{"google.com"}}, setRun: func(args ...string) error {
		called = len(args) >= 3 && args[0] == "add" && args[2] == "198.18.0.2"
		return nil
	}}
	answer := &dns.Msg{MsgHdr: dns.MsgHdr{Rcode: dns.RcodeSuccess}, Answer: []dns.RR{&dns.A{Hdr: dns.RR_Header{Name: "google.com.", Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: uint32(time.Minute.Seconds())}, A: net.ParseIP("198.18.0.2").To4()}}}
	p.observeDNS("google.com", answer)
	if !called {
		t.Fatal("fake IP was not added to the routing set")
	}
}

func TestProxyWhitelistRejectsURLPaths(t *testing.T) {
	for _, value := range []string{"https://google.com", "google.com/path", "google.com:443", "localhost"} {
		if _, err := normalizeProxyWhitelist(proxyWhitelistConfig{Domains: []string{value}, Group: "PROXY"}); err == nil {
			t.Fatalf("accepted %q", value)
		}
	}
}
