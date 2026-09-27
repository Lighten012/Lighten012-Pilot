package main

import (
	"net/netip"
	"path/filepath"
	"testing"
	"time"
)

func dhcpTestRequest(mac byte, message byte, ip netip.Addr) []byte {
	data := make([]byte, 240)
	data[0], data[1], data[2] = 1, 1, 6
	data[28], data[29], data[30], data[31], data[32], data[33] = 2, 0, 0, 0, 0, mac
	copy(data[236:240], []byte{99, 130, 83, 99})
	data = appendDHCPOption(data, 53, []byte{message})
	if ip.IsValid() {
		data = appendDHCPOption(data, 50, ip.AsSlice())
	}
	return append(data, 255)
}

func TestDHCPLeaseFlow(t *testing.T) {
	config, err := makeDHCPConfig(netip.MustParsePrefix("10.0.0.1/24"))
	if err != nil || dhcpAddr(config.Start).String() != "10.0.0.100" || dhcpAddr(config.End).String() != "10.0.0.200" {
		t.Fatalf("unexpected DHCP pool: %+v %v", config, err)
	}
	path := filepath.Join(t.TempDir(), "leases.json")
	s := &dhcpServer{config: config, path: path, clientPort: 68, leases: map[string]dhcpLease{}, offers: map[string]dhcpOfferState{}, declined: map[netip.Addr]time.Time{}}
	offer1, _ := s.handle(dhcpTestRequest(1, dhcpDiscover, netip.Addr{}))
	offer2, _ := s.handle(dhcpTestRequest(2, dhcpDiscover, netip.Addr{}))
	if offer1[242] != dhcpOffer || offer2[242] != dhcpOffer ||
		netip.AddrFrom4([4]byte(offer1[16:20])).String() != "10.0.0.100" ||
		netip.AddrFrom4([4]byte(offer2[16:20])).String() != "10.0.0.101" {
		t.Fatal("DHCP offers reused an address")
	}
	ip := netip.MustParseAddr("10.0.0.100")
	ack, _ := s.handle(dhcpTestRequest(1, dhcpRequest, ip))
	if ack[242] != dhcpACK {
		t.Fatal("DHCP request was not acknowledged")
	}
	loaded, err := loadDHCPLeases(path, config)
	if err != nil || len(loaded) != 1 {
		t.Fatalf("lease was not persisted: %+v %v", loaded, err)
	}
	nak, _ := s.handle(dhcpTestRequest(2, dhcpRequest, ip))
	if nak[242] != dhcpNAK {
		t.Fatal("address already leased to another client was accepted")
	}
	renew := dhcpTestRequest(1, dhcpRequest, netip.Addr{})
	copy(renew[12:16], ip.AsSlice())
	renewed, destination := s.handle(renew)
	if renewed[242] != dhcpACK || destination.String() != "10.0.0.100:68" {
		t.Fatalf("unicast lease renewal failed: type=%d destination=%v", renewed[242], destination)
	}
	release := dhcpTestRequest(1, dhcpRelease, netip.Addr{})
	copy(release[12:16], ip.AsSlice())
	s.handle(release)
	loaded, err = loadDHCPLeases(path, config)
	if err != nil || len(loaded) != 0 {
		t.Fatalf("released lease remained: %+v %v", loaded, err)
	}
	bad := dhcpTestRequest(1, dhcpDiscover, netip.Addr{})[:238]
	if parseDHCPPacket(bad).valid {
		t.Fatal("malformed DHCP packet was accepted")
	}
}

func TestDHCPPoolAvoidsRouterAddress(t *testing.T) {
	for _, input := range []struct{ lan, start, end string }{
		{"10.0.0.150/24", "10.0.0.151", "10.0.0.200"},
		{"10.0.0.1/30", "10.0.0.2", "10.0.0.2"},
	} {
		config, err := makeDHCPConfig(netip.MustParsePrefix(input.lan))
		if err != nil || dhcpAddr(config.Start).String() != input.start || dhcpAddr(config.End).String() != input.end {
			t.Fatalf("unexpected pool for %s: %+v %v", input.lan, config, err)
		}
	}
}
