package main

import (
	"context"
	"net/netip"
	"path/filepath"
	"testing"
	"time"

	"github.com/miekg/dns"
)

func TestMACBoundNameFollowsTargetDHCPLease(t *testing.T) {
	config, err := normalize(Config{Upstream: "119.29.29.29", Records: []Record{
		{Name: "Pilot.Home", Type: "a", MAC: "AA-BB-CC-DD-EE-01"},
	}})
	if err != nil || config.Records[0].MAC != "aa:bb:cc:dd:ee:01" {
		t.Fatalf("normalize target MAC: %+v %v", config, err)
	}
	path := filepath.Join(t.TempDir(), "config.json")
	if err := saveConfig(path, config); err != nil {
		t.Fatal(err)
	}
	config, err = loadConfig(path)
	if err != nil || len(config.Records) != 1 || config.Records[0].Value != "" {
		t.Fatalf("reload MAC binding: %+v %v", config, err)
	}
	pool, _ := makeDHCPConfig(netip.MustParsePrefix("10.0.0.1/24"))
	server := &dhcpServer{config: pool, leases: map[string]dhcpLease{
		"aa:bb:cc:dd:ee:01": {MAC: "aa:bb:cc:dd:ee:01", IP: "10.0.0.100", Expires: time.Now().Add(time.Hour).Unix()},
	}}
	service := &dhcpService{active: server}
	r := newResolver(config)
	r.ipForMAC = service.ipForMAC
	query := new(dns.Msg)
	query.SetQuestion("pilot.home.", dns.TypeA)
	check := func(expected string) {
		t.Helper()
		got := r.resolve(context.Background(), query, "udp")
		if len(got.Answer) != 1 || got.Answer[0].(*dns.A).A.String() != expected {
			t.Fatalf("expected target IP %s: %+v", expected, got)
		}
	}
	check("10.0.0.100")
	server.mu.Lock()
	server.leases["aa:bb:cc:dd:ee:01"] = dhcpLease{MAC: "aa:bb:cc:dd:ee:01", IP: "10.0.0.102", Expires: time.Now().Add(time.Hour).Unix()}
	server.mu.Unlock()
	check("10.0.0.102")
	server.mu.Lock()
	server.leases["aa:bb:cc:dd:ee:01"] = dhcpLease{MAC: "aa:bb:cc:dd:ee:01", IP: "10.0.0.102", Expires: time.Now().Add(-time.Second).Unix()}
	server.mu.Unlock()
	if got := r.resolve(context.Background(), query, "udp"); got.Rcode != dns.RcodeServerFailure || len(got.Answer) != 0 {
		t.Fatalf("expired target lease should fail: %+v", got)
	}
	query.SetQuestion("pilot.home.", dns.TypeAAAA)
	if got := r.resolve(context.Background(), query, "udp"); got.Rcode != dns.RcodeSuccess || len(got.Answer) != 0 || !got.Authoritative {
		t.Fatalf("unconfigured type should be local NODATA: %+v", got)
	}
}

func TestMACBindingValidation(t *testing.T) {
	for _, records := range [][]Record{
		{{Name: "pilot.home", Type: "A", MAC: "not-a-mac"}},
		{{Name: "pilot.home", Type: "AAAA", MAC: "aa:bb:cc:dd:ee:01"}},
		{{Name: "pilot.home", Type: "A", Value: "1.1.1.1", MAC: "aa:bb:cc:dd:ee:01"}},
		{{Name: "pilot.home", Type: "A", Value: "10.0.0.1"}, {Name: "pilot.home", Type: "A", MAC: "aa:bb:cc:dd:ee:01"}},
	} {
		if _, err := normalize(Config{Upstream: "119.29.29.29", Records: records}); err == nil {
			t.Fatalf("invalid binding accepted: %+v", records)
		}
	}
}
