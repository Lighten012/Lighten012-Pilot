//go:build linux

package main

import (
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestDHCPWire(t *testing.T) {
	iface, namespace := os.Getenv("PILOT_DHCP_TEST_IFACE"), os.Getenv("PILOT_DHCP_TEST_NETNS")
	if iface == "" || namespace == "" {
		t.Skip("requires an isolated network namespace and veth pair")
	}
	testDir := t.TempDir()
	path := filepath.Join(testDir, "leases.json")
	start := func() *dhcpService {
		service := newDHCPService(path, 67)
		change, err := service.stage(iface, netip.MustParsePrefix("10.245.1.1/24"))
		if err != nil {
			t.Fatal(err)
		}
		change.commit()
		return service
	}
	client := func() {
		cmd := exec.Command("ip", "netns", "exec", namespace, "dhclient", "-1", "-v", "-sf", "/bin/true", "-pf", filepath.Join(testDir, "client.pid"), "-lf", filepath.Join(testDir, "client.leases"), "pilodc0")
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("DHCP client: %v\n%s", err, output)
		}
		t.Logf("DHCP client: %s", output)
	}
	service := start()
	client()
	service.close()
	clientLease, err := os.ReadFile(filepath.Join(testDir, "client.leases"))
	if err != nil || !strings.Contains(string(clientLease), "option routers 10.245.1.1;") || !strings.Contains(string(clientLease), "option domain-name-servers 10.245.1.1;") {
		t.Fatalf("client did not receive LAN gateway and DNS: %v\n%s", err, clientLease)
	}
	config, _ := makeDHCPConfig(netip.MustParsePrefix("10.245.1.1/24"))
	leases, err := loadDHCPLeases(path, config)
	if err != nil || len(leases) != 1 {
		t.Fatalf("DHCP leases not saved: %+v %v", leases, err)
	}
	for _, lease := range leases {
		if lease.IP != "10.245.1.100" {
			t.Fatalf("unexpected offered address: %+v", lease)
		}
	}
	service = start()
	defer service.close()
	client()
}
