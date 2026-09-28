package network

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDeviceProxyMigratesAndPersistsSelection(t *testing.T) {
	dir := t.TempDir()
	legacy := filepath.Join(dir, "mihomo.yaml.whitelist.json")
	if err := os.WriteFile(legacy, []byte(`{"group":"Main","domains":["example.com"]}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacy+".devices.json", []byte(`{"aa:bb:cc:dd:ee:ff":true}`), 0600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "device-proxy.json")
	proxy, err := NewDeviceProxy(path, legacy)
	if err != nil {
		t.Fatal(err)
	}
	if proxy.Group() != "Main" || !proxy.Devices()["aa:bb:cc:dd:ee:ff"] {
		t.Fatalf("migration: %q %#v", proxy.Group(), proxy.Devices())
	}
	if err := proxy.SetDevice("aa:bb:cc:dd:ee:ff", false); err != nil {
		t.Fatal(err)
	}
	reloaded, err := NewDeviceProxy(path, legacy)
	if err != nil {
		t.Fatal(err)
	}
	if len(reloaded.Devices()) != 0 {
		t.Fatalf("device toggle not persisted: %#v", reloaded.Devices())
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "example.com") {
		t.Fatal("legacy domains should not be copied")
	}
}

func TestDeviceProxyRulesOnlyTargetEnabledMAC(t *testing.T) {
	proxy := &DeviceProxy{lan: "eth1", subnet: "10.0.0.0/24"}
	var rules []string
	proxy.iptRun = func(args ...string) error { rules = append(rules, strings.Join(args, " ")); return nil }
	if err := proxy.applyDeviceRules(map[string]bool{"aa:bb:cc:dd:ee:ff": true}); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(rules, "\n")
	for _, wanted := range []string{"--mac-source aa:bb:cc:dd:ee:ff -p tcp -j REDIRECT --to-ports 7894", "--mac-source aa:bb:cc:dd:ee:ff -p udp -j TPROXY --on-port 7895"} {
		if !strings.Contains(joined, wanted) {
			t.Errorf("missing %q in %s", wanted, joined)
		}
	}
	if strings.Contains(joined, "--match-set") || strings.Contains(joined, "7893") {
		t.Fatal("whitelist rule remains")
	}
}
