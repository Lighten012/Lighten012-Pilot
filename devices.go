package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/netip"
	"os/exec"
	"sort"
	"strings"
	"time"
)

type lanDevice struct {
	IP    string `json:"ip"`
	MAC   string `json:"mac"`
	State string `json:"state"`
}

func (m *networkManager) lanDevices() ([]lanDevice, error) {
	m.mu.Lock()
	if m.roles == nil {
		m.mu.Unlock()
		return []lanDevice{}, nil
	}
	lan := m.roles.LAN
	m.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, "/usr/sbin/ip", "-j", "-4", "neigh", "show", "dev", lan).CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("读取 LAN 邻居表: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return parseLANDevices(output)
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
