package firewall

import (
	"net/netip"
	"strings"
	"testing"
)

func forwardConfig() Config {
	c := Default()
	c.Enabled = true
	c.Forwards = []Forward{{Enabled: true, Protocol: "tcp", ExternalPort: 18080, Target: "10.233.0.2", InternalPort: 80}}
	return c
}
func TestForwardValidation(t *testing.T) {
	c := forwardConfig()
	s, _ := fixture()
	n, e := Normalize(c)
	if e != nil {
		t.Fatal(e)
	}
	n, e = Bind(n, s)
	if e != nil {
		t.Fatal(e)
	}
	c.Forwards = append(c.Forwards, c.Forwards[0])
	if _, e = Normalize(c); e == nil {
		t.Fatal("duplicate port")
	}
	c.Forwards[1].Protocol = "udp"
	if _, e = Normalize(c); e != nil {
		t.Fatal(e)
	}
	for _, target := range []string{"10.233.0.1", "10.233.0.0", "10.233.0.255", "10.234.0.1"} {
		c = forwardConfig()
		c.Forwards[0].Target = target
		if _, e = Bind(c, s); e == nil {
			t.Fatal("invalid target", target)
		}
	}
	c = forwardConfig()
	c.Forwards[0].Source = "1.2.3.4\nCOMMIT"
	if _, e = Normalize(c); e == nil {
		t.Fatal("source injection")
	}
	text := Render(n)
	for _, part := range []string{"-i wan0 -m addrtype --dst-type LOCAL -p tcp --dport 18080", "DNAT --to-destination 10.233.0.2:80", "--ctstate DNAT --ctorigdstport 18080"} {
		if !strings.Contains(text, part) {
			t.Fatal(text)
		}
	}
	n.Forwards[0].Enabled = false
	if strings.Contains(Render(n), "--to-destination") {
		t.Fatal("disabled mapping rendered")
	}
	n.Enabled = false
	n.Forwards[0].Enabled = true
	if strings.Contains(Render(n), "--to-destination") {
		t.Fatal("mapping active while firewall disabled")
	}
}
func TestListenerConflicts(t *testing.T) {
	wan := map[netip.Addr]bool{netip.MustParseAddr("10.234.0.2"): true}
	f := forwardConfig().Forwards
	for _, addr := range []string{"00000000:46A0", "0200EA0A:46A0", "00000000000000000000000000000000:46A0"} {
		if e := listenerConflict("0: "+addr+" 00000000:0000 0A", "tcp", wan, f); e == nil {
			t.Fatal("missed conflict", addr)
		}
	}
	if e := listenerConflict("0: 0100E90A:46A0 00000000:0000 0A", "tcp", wan, f); e != nil {
		t.Fatal("LAN-only socket blocks WAN", e)
	}
	if e := listenerConflict("0: 00000000:46A0 00000000:0000 01", "tcp", wan, f); e != nil {
		t.Fatal("connected socket", e)
	}
}
