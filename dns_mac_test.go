package main

import (
	"context"
	"net/netip"
	"path/filepath"
	"testing"
	"time"

	"github.com/miekg/dns"
)

func TestDeviceDNSRecordsFollowDHCPLease(t *testing.T) {
	config, err := normalize(Config{Upstream: "119.29.29.29", Records: []Record{
		{Name: "pilot.home", Type: "A", Value: "10.0.0.1"},
		{Name: "pilot.home", Type: "A", Value: "10.0.0.9", MAC: "AA-BB-CC-DD-EE-01"},
		{Name: "pilot.home", Type: "AAAA", Value: "fd00::9", MAC: "aa:bb:cc:dd:ee:01"},
	}})
	if err != nil || config.Records[1].MAC != "aa:bb:cc:dd:ee:01" {
		t.Fatalf("normalize MAC: %+v %v", config, err)
	}
	path := filepath.Join(t.TempDir(), "config.json")
	if err := saveConfig(path, config); err != nil {
		t.Fatal(err)
	}
	config, err = loadConfig(path)
	if err != nil || len(config.Records) != 3 {
		t.Fatalf("reload MAC records: %+v %v", config, err)
	}
	pool, _ := makeDHCPConfig(netip.MustParsePrefix("10.0.0.1/24"))
	server := &dhcpServer{config: pool, leases: map[string]dhcpLease{
		"aa:bb:cc:dd:ee:01": {MAC: "aa:bb:cc:dd:ee:01", IP: "10.0.0.100", Expires: time.Now().Add(time.Hour).Unix()},
	}}
	service := &dhcpService{active: server}
	r := newResolver(config)
	r.macForIP = service.macForIP
	query := new(dns.Msg)
	query.SetQuestion("pilot.home.", dns.TypeA)
	answer := func(ip string) *dns.Msg {
		return r.resolve(context.Background(), query, "udp", netip.MustParseAddr(ip))
	}
	if got := answer("10.0.0.100"); len(got.Answer) != 1 || got.Answer[0].(*dns.A).A.String() != "10.0.0.9" {
		t.Fatalf("device override: %+v", got)
	}
	if got := answer("10.0.0.101"); len(got.Answer) != 1 || got.Answer[0].(*dns.A).A.String() != "10.0.0.1" {
		t.Fatalf("default record: %+v", got)
	}
	query.SetQuestion("pilot.home.", dns.TypeAAAA)
	if got := answer("10.0.0.100"); len(got.Answer) != 1 || got.Answer[0].(*dns.AAAA).AAAA.String() != "fd00::9" {
		t.Fatalf("device-only type: %+v", got)
	}
	if got := answer("10.0.0.101"); len(got.Answer) != 0 || !got.Authoritative {
		t.Fatalf("default NODATA: %+v", got)
	}
	server.mu.Lock()
	server.leases["aa:bb:cc:dd:ee:01"] = dhcpLease{MAC: "aa:bb:cc:dd:ee:01", IP: "10.0.0.102", Expires: time.Now().Add(time.Hour).Unix()}
	server.mu.Unlock()
	query.SetQuestion("pilot.home.", dns.TypeA)
	if got := answer("10.0.0.100"); got.Answer[0].(*dns.A).A.String() != "10.0.0.1" {
		t.Fatalf("stale IP kept device override: %+v", got)
	}
	if got := answer("10.0.0.102"); got.Answer[0].(*dns.A).A.String() != "10.0.0.9" {
		t.Fatalf("new IP lost device override: %+v", got)
	}
	server.mu.Lock()
	server.leases["aa:bb:cc:dd:ee:01"] = dhcpLease{MAC: "aa:bb:cc:dd:ee:01", IP: "10.0.0.102", Expires: time.Now().Add(-time.Second).Unix()}
	server.mu.Unlock()
	if got := answer("10.0.0.102"); got.Answer[0].(*dns.A).A.String() != "10.0.0.1" {
		t.Fatalf("expired lease kept device override: %+v", got)
	}
}

func TestDeviceDNSRecordDuplicateValidation(t *testing.T) {
	_, err := normalize(Config{Upstream: "119.29.29.29", Records: []Record{
		{Name: "pilot.home", Type: "A", Value: "10.0.0.1", MAC: "AA-BB-CC-DD-EE-01"},
		{Name: "pilot.home", Type: "A", Value: "10.0.0.2", MAC: "aa:bb:cc:dd:ee:01"},
	}})
	if err == nil {
		t.Fatal("duplicate MAC record accepted")
	}
	_, err = normalize(Config{Upstream: "119.29.29.29", Records: []Record{{Name: "pilot.home", Type: "A", Value: "10.0.0.1", MAC: "not-a-mac"}}})
	if err == nil {
		t.Fatal("invalid MAC accepted")
	}
}
