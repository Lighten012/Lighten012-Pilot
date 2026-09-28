package network

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"slices"
	"sync"

	"github.com/Lighten012/Lighten012-Pilot/internal/storage"
)

const (
	proxyNATChain   = "PILOT_PROXY"
	proxyInputChain = "PILOT_PROXY_IN"
	proxyUDPChain   = "PILOT_PROXY_UDP"
	proxyUDPFwMark  = "0x102/0x102"
	proxyUDPTable   = "102"
)

type deviceProxyState struct {
	Group   string          `json:"group"`
	Devices map[string]bool `json:"devices"`
}

// DeviceProxy selects which LAN MAC addresses send all public TCP/UDP traffic to Mihomo.
type DeviceProxy struct {
	mu     sync.RWMutex
	state  deviceProxyState
	path   string
	active bool
	lan    string
	wan    string
	subnet string
	iptRun func(...string) error
	ipRun  func(...string) error
}

func NewDeviceProxy(path, legacyPath string) (*DeviceProxy, error) {
	p := &DeviceProxy{path: path, iptRun: runIPTables, ipRun: runIPCommand}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		var old struct {
			Group string `json:"group"`
		}
		if saved, readErr := os.ReadFile(legacyPath); readErr == nil {
			if err := json.Unmarshal(saved, &old); err != nil {
				return nil, err
			}
		} else if !errors.Is(readErr, os.ErrNotExist) {
			return nil, readErr
		}
		p.state.Group = old.Group
		p.state.Devices = map[string]bool{}
		if saved, readErr := os.ReadFile(legacyPath + ".devices.json"); readErr == nil {
			if err := json.Unmarshal(saved, &p.state.Devices); err != nil {
				return nil, err
			}
		} else if !errors.Is(readErr, os.ErrNotExist) {
			return nil, readErr
		}
		if err := p.save(p.state); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	} else if err := json.Unmarshal(data, &p.state); err != nil {
		return nil, err
	}
	if p.state.Devices == nil {
		p.state.Devices = map[string]bool{}
	}
	for mac, enabled := range p.state.Devices {
		parsed, err := net.ParseMAC(mac)
		if err != nil || len(parsed) != 6 || parsed.String() != mac || !enabled {
			return nil, fmt.Errorf("invalid proxied device %q", mac)
		}
	}
	return p, nil
}

func (p *DeviceProxy) save(state deviceProxyState) error {
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	return storage.WriteAtomic(p.path, append(data, '\n'), 0600)
}

func (p *DeviceProxy) Group() string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.state.Group
}

func (p *DeviceProxy) SetGroup(group string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	next := p.state
	next.Group = group
	if err := p.save(next); err != nil {
		return err
	}
	p.state = next
	return nil
}

func (p *DeviceProxy) Devices() map[string]bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	copy := make(map[string]bool, len(p.state.Devices))
	for mac, enabled := range p.state.Devices {
		copy[mac] = enabled
	}
	return copy
}

func (p *DeviceProxy) SetDevice(mac string, enabled bool) error {
	parsed, err := net.ParseMAC(mac)
	if err != nil || len(parsed) != 6 {
		return errors.New("invalid MAC address")
	}
	mac = parsed.String()
	p.mu.Lock()
	defer p.mu.Unlock()
	if enabled && p.state.Group == "" {
		return errors.New("请先选择设备代理组")
	}
	next := deviceProxyState{Group: p.state.Group, Devices: make(map[string]bool, len(p.state.Devices))}
	for key, value := range p.state.Devices {
		next.Devices[key] = value
	}
	if enabled {
		next.Devices[mac] = true
	} else {
		delete(next.Devices, mac)
	}
	if p.active {
		if err := p.applyDeviceRules(next.Devices); err != nil {
			_ = p.applyDeviceRules(p.state.Devices)
			return err
		}
	}
	if err := p.save(next); err != nil {
		if p.active {
			_ = p.applyDeviceRules(p.state.Devices)
		}
		return err
	}
	p.state = next
	return nil
}

func (p *DeviceProxy) applyDeviceRules(devices map[string]bool) error {
	for _, table := range []string{"nat", "mangle"} {
		chain := proxyNATChain
		if table == "mangle" {
			chain = proxyUDPChain
		}
		if err := p.iptRun("-t", table, "-F", chain); err != nil {
			return err
		}
		for _, subnet := range []string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "127.0.0.0/8", "169.254.0.0/16", "224.0.0.0/4"} {
			if err := p.iptRun("-t", table, "-A", chain, "-d", subnet, "-j", "RETURN"); err != nil {
				return err
			}
		}
	}
	macs := make([]string, 0, len(devices))
	for mac := range devices {
		macs = append(macs, mac)
	}
	slices.Sort(macs)
	for _, mac := range macs {
		if err := p.iptRun("-t", "nat", "-A", proxyNATChain, "-i", p.lan, "-s", p.subnet, "-m", "mac", "--mac-source", mac, "-p", "tcp", "-j", "REDIRECT", "--to-ports", "7894"); err != nil {
			return err
		}
		if err := p.iptRun("-t", "mangle", "-A", proxyUDPChain, "-i", p.lan, "-s", p.subnet, "-m", "mac", "--mac-source", mac, "-p", "udp", "-j", "TPROXY", "--on-port", "7895", "--tproxy-mark", proxyUDPFwMark); err != nil {
			return err
		}
	}
	return nil
}

func (p *DeviceProxy) Apply(roles Roles, lan netip.Prefix) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, item := range []struct{ table, chain string }{{"nat", proxyNATChain}, {"filter", proxyInputChain}, {"mangle", proxyUDPChain}} {
		if err := p.iptRun("-t", item.table, "-S", item.chain); err != nil {
			if err := p.iptRun("-t", item.table, "-N", item.chain); err != nil {
				return err
			}
		}
		if err := p.iptRun("-t", item.table, "-F", item.chain); err != nil {
			return err
		}
	}
	p.lan, p.wan, p.subnet = roles.LAN, roles.WAN, lan.Masked().String()
	if err := p.applyDeviceRules(p.state.Devices); err != nil {
		return err
	}
	for _, rule := range [][]string{
		{"-t", "filter", "-A", proxyInputChain, "-i", roles.WAN, "-p", "tcp", "--dport", "7894", "-j", "DROP"},
		{"-t", "filter", "-A", proxyInputChain, "-i", roles.WAN, "-p", "udp", "--dport", "7895", "-j", "DROP"},
	} {
		if err := p.iptRun(rule...); err != nil {
			return err
		}
	}
	if err := p.ipRun("route", "replace", "local", "0.0.0.0/0", "dev", "lo", "table", proxyUDPTable); err != nil {
		return err
	}
	_ = p.ipRun("rule", "del", "fwmark", proxyUDPFwMark, "lookup", proxyUDPTable, "priority", "1102")
	if err := p.ipRun("rule", "add", "fwmark", proxyUDPFwMark, "lookup", proxyUDPTable, "priority", "1102"); err != nil {
		return err
	}
	for _, item := range []struct{ table, parent, child string }{{"nat", "PREROUTING", proxyNATChain}, {"filter", "INPUT", proxyInputChain}, {"mangle", "PREROUTING", proxyUDPChain}} {
		if err := p.iptRun("-t", item.table, "-C", item.parent, "-j", item.child); err != nil {
			if err := p.iptRun("-t", item.table, "-I", item.parent, "1", "-j", item.child); err != nil {
				return err
			}
		}
	}
	p.active = true
	return nil
}

func (p *DeviceProxy) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.active {
		return nil
	}
	var errs []error
	for _, item := range []struct{ table, parent, child string }{{"nat", "PREROUTING", proxyNATChain}, {"filter", "INPUT", proxyInputChain}, {"mangle", "PREROUTING", proxyUDPChain}} {
		if err := p.iptRun("-t", item.table, "-C", item.parent, "-j", item.child); err == nil {
			errs = append(errs, p.iptRun("-t", item.table, "-D", item.parent, "-j", item.child))
		}
		errs = append(errs, p.iptRun("-t", item.table, "-F", item.child), p.iptRun("-t", item.table, "-X", item.child))
	}
	errs = append(errs, p.ipRun("rule", "del", "fwmark", proxyUDPFwMark, "lookup", proxyUDPTable, "priority", "1102"))
	errs = append(errs, p.ipRun("route", "del", "local", "0.0.0.0/0", "dev", "lo", "table", proxyUDPTable))
	p.active = false
	return errors.Join(errs...)
}
