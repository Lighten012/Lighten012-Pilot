package main

import "testing"

func TestParseLANDevices(t *testing.T) {
	data := []byte(`[
		{"dst":"10.0.0.20","lladdr":"aa:bb:cc:dd:ee:20","state":["REACHABLE"]},
		{"dst":"10.0.0.2","lladdr":"aa:bb:cc:dd:ee:02","state":["STALE"]},
		{"dst":"10.0.0.3","state":["INCOMPLETE"]},
		{"dst":"10.0.0.4","lladdr":"aa:bb:cc:dd:ee:04","state":["FAILED"]},
		{"dst":"fe80::1","lladdr":"aa:bb:cc:dd:ee:05","state":["REACHABLE"]}
	]`)
	devices, err := parseLANDevices(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(devices) != 2 || devices[0].IP != "10.0.0.2" || devices[0].State != "STALE" || devices[1].IP != "10.0.0.20" {
		t.Fatalf("unexpected LAN devices: %+v", devices)
	}
}

func TestLANDevicePresenceUsesCurrentLeaseMAC(t *testing.T) {
	leases := []dhcpLease{{IP: "10.0.0.105", MAC: "bc:24:11:10:cd:c7"}, {IP: "10.0.0.106", MAC: "aa:bb:cc:dd:ee:06"}}
	neighbors := []lanDevice{{IP: "10.0.0.2", MAC: "bc:24:11:10:cd:c7", State: "STALE"}, {IP: "10.0.0.105", MAC: "bc:24:11:ca:d6:60", State: "STALE"}}
	devices := lanDeviceCandidates(leases, neighbors)
	if len(devices) != 2 || devices[0].MAC != "bc:24:11:10:cd:c7" {
		t.Fatalf("stale MAC displaced current lease: %+v", devices)
	}
	devices = classifyLANDevices(devices, map[string]arpProbe{"10.0.0.105": {MAC: "bc:24:11:10:cd:c7"}, "10.0.0.106": {}})
	if devices[0].State != "ONLINE" || devices[1].State != "OFFLINE" {
		t.Fatalf("wrong presence states: %+v", devices)
	}
}
