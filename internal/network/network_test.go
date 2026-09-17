package network

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func fixture() (Config, Inventory) {
	w := Port{Interface: "wan0", Mode: "dhcp"}
	return Config{WAN: w, LAN: Port{Interface: "lan0", Mode: "static", Address: "192.168.60.1/24"}}, Inventory{Devices: []Device{{Name: "wan0", Current: &w, Protected: true, Addresses: []string{"192.168.50.136/24"}}, {Name: "lan0"}}, Routes: []Route{{Dst: "192.168.50.0/24", Dev: "wan0"}}, Revision: "one"}
}
func TestValidation(t *testing.T) {
	c, inv := fixture()
	if e := Validate(c, inv); e != nil {
		t.Fatal(e)
	}
	for _, tc := range []struct {
		name string
		edit func(*Config)
	}{
		{"same interface", func(c *Config) { c.LAN.Interface = "wan0" }},
		{"injection", func(c *Config) { c.LAN.Interface = "lan0;reboot" }},
		{"overlap", func(c *Config) { c.LAN.Address = "192.168.50.1/24" }},
		{"network address", func(c *Config) { c.LAN.Address = "192.168.60.0/24" }},
		{"broadcast", func(c *Config) { c.LAN.Address = "192.168.60.255/24" }},
		{"ipv6", func(c *Config) { c.LAN.Address = "fd00::1/64" }},
		{"dhcp stale fields", func(c *Config) { c.WAN.Address = "192.168.50.136/24" }},
		{"protected management", func(c *Config) {
			c.WAN.Mode = "static"
			c.WAN.Address = "192.168.50.10/24"
			c.WAN.Gateway = "192.168.50.1"
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bad := c
			tc.edit(&bad)
			if Validate(bad, inv) == nil {
				t.Fatal("accepted invalid configuration")
			}
		})
	}
}
func TestParserRefusesUnknownAndPreservesIPv6(t *testing.T) {
	s := "auto lan0\niface lan0 inet static\n address 10.0.0.1/24\niface lan0 inet6 auto\n"
	p, start, end, e := parseStanza(s, "lan0")
	if e != nil || p.Address != "10.0.0.1/24" || start != 1 || end != 3 {
		t.Fatal(p, start, end, e)
	}
	_, _, _, e = parseStanza("iface lan0 inet static\n address 10.0.0.1/24\n post-up reboot\n", "lan0")
	if e == nil {
		t.Fatal("must refuse arbitrary hooks")
	}
}

type fakeBackend struct {
	mu                 sync.Mutex
	inv                Inventory
	files              []File
	fail               bool
	applies, rollbacks int
}

func (f *fakeBackend) Inspect() (Inventory, []File, error) { return f.inv, f.files, nil }
func (f *fakeBackend) Prepare(c Config, inv Inventory, files []File) (Plan, []File, error) {
	if e := Validate(c, inv); e != nil {
		return Plan{}, nil, e
	}
	return Plan{Config: c, Revision: inv.Revision, Token: Fingerprint(c), Changes: []Change{{Interface: "lan0", After: c.LAN, Changed: true}}}, []File{{Path: "new-file", Exists: true, Mode: 0644, Content: Render(c.LAN)}}, nil
}
func (f *fakeBackend) Write(files []File) error { return nil }
func (f *fakeBackend) Activate(c []Change, back bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if back {
		f.rollbacks++
		return nil
	}
	f.applies++
	if f.fail {
		return errors.New("injected failure")
	}
	return nil
}
func setup(t *testing.T) (*Manager, *fakeBackend, Config) {
	t.Helper()
	c, inv := fixture()
	f := &fakeBackend{inv: inv}
	m, e := NewManager(f, t.TempDir(), 80*time.Millisecond)
	if e != nil {
		t.Fatal(e)
	}
	return m, f, c
}
func waitPhase(t *testing.T, m *Manager, want string) {
	t.Helper()
	until := time.Now().Add(time.Second)
	for time.Now().Before(until) {
		s, _ := m.Status()
		if want == "" && s.Pending == nil {
			return
		}
		if s.Pending != nil && s.Pending.Phase == want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("phase not reached", want)
}
func TestApplyConfirmAndStalePreview(t *testing.T) {
	m, _, c := setup(t)
	p, e := m.Preview(c)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = m.Apply(ApplyRequest{Config: c, Revision: "stale", Token: p.Token}); e == nil {
		t.Fatal("stale accepted")
	}
	id, e := m.Apply(ApplyRequest{c, p.Revision, p.Token})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = m.Apply(ApplyRequest{c, p.Revision, p.Token}); e == nil {
		t.Fatal("concurrent apply accepted")
	}
	waitPhase(t, m, "awaiting_confirmation")
	if e = m.Confirm("wrong"); e == nil {
		t.Fatal("wrong id accepted")
	}
	if e = m.Confirm(id); e != nil {
		t.Fatal(e)
	}
	s, _ := m.Status()
	if s.Saved == nil || *s.Saved != c || s.Pending != nil {
		t.Fatal("confirmation lost")
	}
}
func TestTimeoutAndCrashRecovery(t *testing.T) {
	for _, restart := range []bool{false, true} {
		m, f, c := setup(t)
		p, _ := m.Preview(c)
		_, e := m.Apply(ApplyRequest{c, p.Revision, p.Token})
		if e != nil {
			t.Fatal(e)
		}
		waitPhase(t, m, "awaiting_confirmation")
		if restart {
			m2, e := NewManager(f, filepath.Dir(m.file), time.Second)
			if e != nil {
				t.Fatal(e)
			}
			if e = m2.Recover(); e != nil {
				t.Fatal(e)
			}
			m = m2
		} else {
			time.Sleep(90 * time.Millisecond)
			m.Tick()
		}
		s, _ := m.Status()
		if s.Pending != nil || s.Saved != nil || f.rollbacks != 1 {
			t.Fatal("rollback failed")
		}
		if _, e := os.Stat(m.file); e != nil {
			t.Fatal(e)
		}
	}
}
func TestApplyFailureRestoresAndNewFileTombstone(t *testing.T) {
	m, f, c := setup(t)
	f.fail = true
	p, old, _, e := m.preview(c)
	if e != nil {
		t.Fatal(e)
	}
	if len(old) != 1 || old[0].Exists {
		t.Fatal("new file not backed up as absent")
	}
	_, e = m.Apply(ApplyRequest{c, p.Revision, p.Token})
	if e != nil {
		t.Fatal(e)
	}
	waitPhase(t, m, "")
	if f.rollbacks != 1 {
		t.Fatal("failed apply did not roll back")
	}
}
