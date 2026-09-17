package services

import (
	"github.com/Lighten012/Lighten012-Pilot/internal/network"
	"strings"
	"testing"
)

func TestValidation(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*Config)
	}{
		{"pool outside LAN", func(c *Config) { c.DHCP.Start = "192.168.50.100" }},
		{"router in pool", func(c *Config) { c.DHCP.Start = "192.168.60.1" }},
		{"broadcast in pool", func(c *Config) { c.DHCP.End = "192.168.60.255" }},
		{"reversed pool", func(c *Config) { c.DHCP.Start = "192.168.60.220" }},
		{"wildcard", func(c *Config) { c.DNS.Records[0].Name = "*.home" }},
		{"injection", func(c *Config) { c.DNS.Upstream = "1.1.1.1\nport=54" }},
		{"family mismatch", func(c *Config) { c.DNS.Records[0].Type = "AAAA" }},
		{"DNS disabled", func(c *Config) { c.DNS.Enabled = false }},
		{"invalid TTL", func(c *Config) { c.DNS.Records[0].TTL = 0 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := testConfig()
			tc.mutate(&c)
			if _, e := Normalize(c); e == nil {
				t.Fatal("accepted invalid config")
			}
		})
	}
	c := testConfig()
	c.DNS.Records[0].Name = " LiGhTeN012.HOME "
	c, e := Normalize(c)
	if e != nil || c.DNS.Records[0].Name != "lighten012.home." {
		t.Fatal(c, e)
	}
	text := DHCPText(c, "/tmp/test")
	for _, want := range []string{"port=0", "interface=lan0", "except-interface=lo", "dhcp-option=option:dns-server,192.168.60.1", "dhcp-option=option:router,192.168.60.1"} {
		if !strings.Contains(text, want) {
			t.Fatal(text)
		}
	}
}
func TestLANProtection(t *testing.T) {
	c := testConfig()
	s := network.Status{Saved: &network.Config{LAN: network.Port{Interface: "lan0", Mode: "static", Address: c.Address}}, Inventory: network.Inventory{Devices: []network.Device{{Name: "lan0", Addresses: []string{c.Address}}}}}
	if e := ValidateLAN(c, s); e != nil {
		t.Fatal(e)
	}
	s.Inventory.Devices[0].Protected = true
	if e := ValidateLAN(c, s); e == nil {
		t.Fatal("protected interface accepted")
	}
	s.Inventory.Devices[0].Protected = false
	c.DNS.Upstream = "192.168.60.1"
	if e := ValidateLAN(c, s); e == nil {
		t.Fatal("self-forward loop accepted")
	}
}
func TestLeaseParsing(t *testing.T) {
	l := ParseLeases("100 aa 192.168.60.2 old *\n300 bb 192.168.60.3 host *\nmalformed\n0 cc 192.168.60.4 * *", 200)
	if len(l) != 2 || l[0].Expires != 300000 || l[1].Expires != 0 {
		t.Fatal(l)
	}
}
func TestDisabledPersistenceAndStalePreview(t *testing.T) {
	state := func() (network.Status, error) {
		return network.Status{Inventory: network.Inventory{Revision: "x"}}, nil
	}
	dir := t.TempDir()
	m, e := NewManager(dir, state)
	if e != nil {
		t.Fatal(e)
	}
	c := Default()
	p, e := m.Preview(c)
	if e != nil {
		t.Fatal(e)
	}
	bad := ApplyRequest{Config: c, Revision: p.Revision, Token: "stale"}
	if e = m.Apply(bad); e == nil {
		t.Fatal("stale token accepted")
	}
	if e = m.Apply(ApplyRequest{Config: p.Config, Revision: p.Revision, Token: p.Token}); e != nil {
		t.Fatal(e)
	}
	m2, e := NewManager(dir, state)
	if e != nil {
		t.Fatal(e)
	}
	s, e := m2.State()
	if e != nil || s.Config.DNS.Upstream != "119.29.29.29" || s.DNSRunning || s.DHCPRunning {
		t.Fatal(s, e)
	}
}
