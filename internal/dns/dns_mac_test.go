package dnsservice

import (
	"context"
	"net/netip"
	"path/filepath"
	"testing"

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
	address := netip.MustParseAddr("10.0.0.100")
	valid := true
	r := newResolver(config)
	r.ipForMAC = func(mac string) (netip.Addr, bool) {
		return address, valid && mac == "aa:bb:cc:dd:ee:01"
	}
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
	address = netip.MustParseAddr("10.0.0.102")
	check("10.0.0.102")
	valid = false
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
