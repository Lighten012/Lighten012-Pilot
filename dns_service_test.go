package main

import (
	"net"
	"net/netip"
	"strconv"
	"testing"
	"time"

	"github.com/miekg/dns"
)

func TestDNSFollowsLANAddress(t *testing.T) {
	probe, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := probe.Addr().(*net.TCPAddr).Port
	probe.Close()

	r := newResolver(Config{Upstream: "119.29.29.29", Records: []Record{{Name: "pilot.home", Type: "A", Value: "10.0.0.1", TTL: 60}}})
	s := newDNSService(r, port)
	defer s.close()
	first := netip.MustParsePrefix("127.0.0.1/8")
	change, err := s.stage(first)
	if err != nil {
		t.Fatal(err)
	}
	change.commit()
	query := new(dns.Msg)
	query.SetQuestion("pilot.home.", dns.TypeA)
	client := &dns.Client{Net: "udp", Timeout: time.Second}
	address := func(ip string) string { return net.JoinHostPort(ip, strconv.Itoa(port)) }
	if answer, _, err := client.Exchange(query, address("127.0.0.1")); err != nil || len(answer.Answer) != 1 {
		t.Fatalf("first LAN DNS: %v %+v", err, answer)
	}
	s.close()
	if _, _, err := client.Exchange(query, address("127.0.0.1")); err == nil {
		t.Fatal("LAN DNS is still listening after stop")
	}
}
