package firewall

import (
	"errors"
	"github.com/Lighten012/Lighten012-Pilot/internal/network"
	"strings"
	"testing"
	"time"
)

type fake struct {
	current    Config
	forwarding string
	fail       bool
}

func (b *fake) Available() error            { return nil }
func (b *fake) Forwarding() (string, error) { return b.forwarding, nil }
func (b *fake) Check(Config) error          { return nil }
func (b *fake) Apply(c Config, baseline string) error {
	if b.fail {
		b.fail = false
		return errors.New("injected partial failure")
	}
	b.current = c
	b.forwarding = baseline
	if c.Enabled {
		b.forwarding = "1"
	}
	return nil
}
func fixture() (network.Status, error) {
	return network.Status{Saved: &network.Config{WAN: network.Port{Interface: "wan0", Mode: "dhcp"}, LAN: network.Port{Interface: "lan0", Mode: "static", Address: "10.233.0.1/24"}}, Inventory: network.Inventory{Devices: []network.Device{{Name: "wan0"}, {Name: "lan0", Addresses: []string{"10.233.0.1/24"}}}}}, nil
}
func TestValidation(t *testing.T) {
	c := Default()
	c.Enabled = true
	c.NAT = true
	c.Rules = []Rule{{Enabled: true, Action: "DROP", Protocol: "tcp", Source: "10.233.0.2", Port: "80-90"}}
	n, e := Normalize(c)
	if e != nil || n.Rules[0].Source != "10.233.0.2/32" {
		t.Fatal(n, e)
	}
	for _, port := range []string{"0", "65536", "90-80", "80\nCOMMIT", "80;reboot"} {
		bad := c
		bad.Rules = append([]Rule{}, c.Rules...)
		bad.Rules[0].Port = port
		if _, e := Normalize(bad); e == nil {
			t.Fatal("accepted", port)
		}
	}
	n.Rules[0].Protocol = "icmp"
	if _, e := Normalize(n); e == nil {
		t.Fatal("icmp port accepted")
	}
	c.Enabled = false
	if _, e := Normalize(c); e == nil {
		t.Fatal("NAT without firewall")
	}
}
func TestRender(t *testing.T) {
	c := Default()
	c.Enabled = true
	c.NAT = true
	s, _ := fixture()
	c, _ = Bind(c, s)
	text := Render(c)
	for _, bad := range []string{"-F INPUT", "-P FORWARD", ":INPUT", ":OUTPUT"} {
		if strings.Contains(text, bad) {
			t.Fatal(text)
		}
	}
	if !strings.Contains(text, "-s 10.233.0.0/24 -o wan0 -j MASQUERADE") || !strings.Contains(text, "-i wan0 -o lan0 -j DROP") {
		t.Fatal(text)
	}
	s.Pending = &network.Pending{}
	if _, e := Bind(c, s); e == nil {
		t.Fatal("pending network accepted")
	}
}
func TestTransactions(t *testing.T) {
	b := &fake{forwarding: "0"}
	dir := t.TempDir()
	m, e := NewManager(b, dir, fixture, time.Minute)
	if e != nil {
		t.Fatal(e)
	}
	c := Default()
	c.Enabled = true
	c.NAT = true
	p, e := m.Preview(c)
	if e != nil {
		t.Fatal(e)
	}
	bad := p
	bad.Token = "stale"
	if _, e = m.Apply(bad); e == nil {
		t.Fatal("stale accepted")
	}
	id, e := m.Apply(p)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = m.Apply(p); e == nil {
		t.Fatal("concurrent accepted")
	}
	if e = m.Confirm(id); e != nil {
		t.Fatal(e)
	}
	m, e = NewManager(b, dir, fixture, time.Minute)
	if e != nil || !b.current.Enabled {
		t.Fatal("confirmed not restored", e)
	}
	c.NAT = false
	p, _ = m.Preview(c)
	id, e = m.Apply(p)
	if e != nil {
		t.Fatal(e)
	}
	m, e = NewManager(b, dir, fixture, time.Minute)
	if e != nil || !b.current.NAT {
		t.Fatal("pending not rolled back", e)
	}
	p, _ = m.Preview(c)
	b.fail = true
	if _, e = m.Apply(p); e == nil || !b.current.NAT {
		t.Fatal("failed application not restored", e)
	}
	p, _ = m.Preview(c)
	id, e = m.Apply(p)
	if e != nil {
		t.Fatal(e)
	}
	m.journal.Pending.Deadline = time.Now().Add(-time.Second).UnixMilli()
	if e = m.Confirm(id); e == nil {
		t.Fatal("expired confirmed")
	}
	m.Tick()
	if !b.current.NAT || m.journal.Pending != nil {
		t.Fatal("timeout not restored")
	}
	c.Enabled = false
	c.NAT = false
	p, _ = m.Preview(c)
	id, e = m.Apply(p)
	if e != nil {
		t.Fatal(e)
	}
	if e = m.Confirm(id); e != nil {
		t.Fatal(e)
	}
	if b.forwarding != "0" {
		t.Fatal("baseline not restored")
	}
}
