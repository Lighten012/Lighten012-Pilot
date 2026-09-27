package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/netip"
	"os/exec"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

type lanDevice struct {
	IP    string `json:"ip"`
	MAC   string `json:"mac"`
	State string `json:"state"`
}

type arpProbe struct {
	MAC string
	Err error
}

var arpReplyMAC = regexp.MustCompile(`(?i)reply from [^\[]+\[([0-9a-f:]{17})\]`)

func (m *networkManager) lanDevices() ([]lanDevice, error) {
	m.mu.Lock()
	if m.roles == nil {
		m.mu.Unlock()
		return []lanDevice{}, nil
	}
	lan := m.roles.LAN
	dhcp := m.dhcp
	m.mu.Unlock()
	var leases []dhcpLease
	if dhcp != nil {
		leases = dhcp.currentLeases()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, "/usr/sbin/ip", "-j", "-4", "neigh", "show", "dev", lan).CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("读取 LAN 邻居表: %w: %s", err, strings.TrimSpace(string(output)))
	}
	neighbors, err := parseLANDevices(output)
	if err != nil {
		return nil, err
	}
	candidates := lanDeviceCandidates(leases, neighbors)
	if len(candidates) == 0 {
		return []lanDevice{}, nil
	}
	probeCtx, stop := context.WithTimeout(context.Background(), 12*time.Second)
	defer stop()
	results := make(map[string]arpProbe, len(candidates))
	var resultsMu sync.Mutex
	var workers sync.WaitGroup
	limit := make(chan struct{}, 24)
	for _, device := range candidates {
		workers.Add(1)
		go func(ip string) {
			defer workers.Done()
			select {
			case limit <- struct{}{}:
				defer func() { <-limit }()
			case <-probeCtx.Done():
				return
			}
			mac, err := probeARP(probeCtx, lan, ip)
			resultsMu.Lock()
			results[ip] = arpProbe{MAC: mac, Err: err}
			resultsMu.Unlock()
		}(device.IP)
	}
	workers.Wait()
	return classifyLANDevices(candidates, results), nil
}

func lanDeviceCandidates(leases []dhcpLease, neighbors []lanDevice) []lanDevice {
	byIP := make(map[string]lanDevice)
	leasedMACs := make(map[string]string)
	for _, lease := range leases {
		ip, ipErr := netip.ParseAddr(lease.IP)
		mac, macErr := net.ParseMAC(lease.MAC)
		if ipErr == nil && ip.Is4() && macErr == nil {
			byIP[ip.String()] = lanDevice{IP: ip.String(), MAC: mac.String()}
			leasedMACs[mac.String()] = ip.String()
		}
	}
	for _, device := range neighbors {
		if currentIP, hasLease := leasedMACs[strings.ToLower(device.MAC)]; hasLease && currentIP != device.IP {
			continue
		}
		if _, leased := byIP[device.IP]; !leased {
			byIP[device.IP] = device
		}
	}
	devices := make([]lanDevice, 0, len(byIP))
	for _, device := range byIP {
		devices = append(devices, device)
	}
	sort.Slice(devices, func(i, j int) bool {
		a, _ := netip.ParseAddr(devices[i].IP)
		b, _ := netip.ParseAddr(devices[j].IP)
		return a.Compare(b) < 0
	})
	return devices
}

func classifyLANDevices(devices []lanDevice, results map[string]arpProbe) []lanDevice {
	for i := range devices {
		probe, found := results[devices[i].IP]
		switch {
		case !found || probe.Err != nil:
			devices[i].State = "UNKNOWN"
		case probe.MAC == "":
			devices[i].State = "OFFLINE"
		case strings.EqualFold(probe.MAC, devices[i].MAC):
			devices[i].State = "ONLINE"
		default:
			devices[i].State = "MISMATCH"
		}
	}
	return devices
}

func probeARP(ctx context.Context, iface, ip string) (string, error) {
	output, err := exec.CommandContext(ctx, "/usr/bin/arping", "-I", iface, "-c", "1", "-w", "2", ip).CombinedOutput()
	if err != nil {
		if exit, ok := err.(*exec.ExitError); ok && exit.ExitCode() == 1 {
			return "", nil
		}
		return "", fmt.Errorf("ARP probe %s: %w: %s", ip, err, strings.TrimSpace(string(output)))
	}
	match := arpReplyMAC.FindSubmatch(output)
	if len(match) != 2 {
		return "", fmt.Errorf("ARP reply lacks MAC for %s", ip)
	}
	mac, err := net.ParseMAC(string(match[1]))
	if err != nil {
		return "", err
	}
	return mac.String(), nil
}

func parseLANDevices(data []byte) ([]lanDevice, error) {
	var entries []struct {
		IP    string   `json:"dst"`
		MAC   string   `json:"lladdr"`
		State []string `json:"state"`
	}
	if err := json.Unmarshal(data, &entries); err != nil {
		return nil, err
	}
	devices := make([]lanDevice, 0, len(entries))
	for _, entry := range entries {
		ip, err := netip.ParseAddr(entry.IP)
		if err != nil || !ip.Is4() {
			continue
		}
		mac, err := net.ParseMAC(entry.MAC)
		if err != nil {
			continue
		}
		state := "UNKNOWN"
		if len(entry.State) > 0 {
			state = strings.ToUpper(entry.State[0])
		}
		if state == "FAILED" || state == "INCOMPLETE" {
			continue
		}
		devices = append(devices, lanDevice{IP: ip.String(), MAC: mac.String(), State: state})
	}
	sort.Slice(devices, func(i, j int) bool {
		a, _ := netip.ParseAddr(devices[i].IP)
		b, _ := netip.ParseAddr(devices[j].IP)
		return a.Compare(b) < 0
	})
	return devices, nil
}
