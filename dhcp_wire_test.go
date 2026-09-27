//go:build linux

package main

import (
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

	// Query from the DHCP client namespace, so DNS sees the leased source IP.
	r := newResolver(Config{Upstream: "119.29.29.29", Records: []Record{
		{Name: "pilot.home", Type: "A", Value: "10.245.1.1", TTL: 60},
		{Name: "pilot.home", Type: "A", Value: "10.245.1.9", TTL: 60, MAC: leasesMAC(leases)},
	}})
	r.macForIP = service.macForIP
	dnsServer := newDNSService(r, 15353)
	defer dnsServer.close()
	dnsChange, err := dnsServer.stage(netip.MustParsePrefix("10.245.1.1/24"))
	if err != nil {
		t.Fatal(err)
	}
	dnsChange.commit()
	if output, err := exec.Command("ip", "netns", "exec", namespace, "ip", "addr", "add", "10.245.1.100/24", "dev", "pilodc0").CombinedOutput(); err != nil {
		t.Fatalf("configure DHCP client address: %v %s", err, output)
	}
	query := func(expected string) {
		t.Helper()
		const script = `import socket,struct,sys
q=b'\x12\x34\x01\x00\x00\x01\x00\x00\x00\x00\x00\x00\x05pilot\x04home\x00'+struct.pack('!HH',1,1)
s=socket.socket(socket.AF_INET,socket.SOCK_DGRAM);s.settimeout(2)
s.sendto(q,('10.245.1.1',15353));reply,_=s.recvfrom(512)
assert reply[:2]==b'\x12\x34' and reply[7]==1,reply.hex()
assert socket.inet_ntoa(reply[-4:])==sys.argv[1],reply.hex()`
		output, err := exec.Command("ip", "netns", "exec", namespace, "python3", "-c", script, expected).CombinedOutput()
		if err != nil {
			t.Fatalf("DNS query expected %s: %v %s", expected, err, output)
		}
	}
	query("10.245.1.9")
	service.active.mu.Lock()
	for mac, lease := range service.active.leases {
		lease.Expires = time.Now().Add(-time.Second).Unix()
		service.active.leases[mac] = lease
	}
	service.active.mu.Unlock()
	query("10.245.1.1")
}

func leasesMAC(leases map[string]dhcpLease) string {
	for mac := range leases {
		return mac
	}
	return ""
}
