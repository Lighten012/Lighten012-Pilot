package network

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/miekg/dns"
	"gopkg.in/yaml.v3"
)

const proxyIPSet = "PILOT_PROXY_IPS"
const proxyNATChain = "PILOT_PROXY"
const proxyInputChain = "PILOT_PROXY_IN"
const proxyQUICChain = "PILOT_PROXY_QUIC"
const proxyUDPChain = "PILOT_PROXY_UDP"
const proxyUDPFwMark = "0x102/0x102"
const proxyUDPTable = "102"

var proxyDomainPattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]*[a-z0-9])?(?:\.[a-z0-9](?:[a-z0-9-]*[a-z0-9])?)+$`)

type proxyWhitelistConfig struct {
	Domains []string `json:"domains"`
	Group   string   `json:"group"`
}

type proxyWhitelist struct {
	mu          sync.RWMutex
	config      proxyWhitelistConfig
	fullDevices map[string]bool
	path        string
	active      bool
	lan         string
	wan         string
	subnet      string
	setRun      func(...string) error
	iptRun      func(...string) error
	ipRun       func(...string) error
}

type ProxyWhitelist = proxyWhitelist
type ProxyWhitelistConfig = proxyWhitelistConfig

func NewProxyWhitelist(path string) (*ProxyWhitelist, error) { return newProxyWhitelist(path) }
func NormalizeProxyWhitelist(input ProxyWhitelistConfig) (ProxyWhitelistConfig, error) {
	return normalizeProxyWhitelist(input)
}
func ApplyWhitelistRules(content []byte, config ProxyWhitelistConfig) ([]byte, error) {
	return applyWhitelistRules(content, config)
}
func (p *proxyWhitelist) Devices() map[string]bool                 { return p.devices() }
func (p *proxyWhitelist) SetDevice(mac string, enabled bool) error { return p.setDevice(mac, enabled) }
func (p *proxyWhitelist) Get() ProxyWhitelistConfig                { return p.get() }
func (p *proxyWhitelist) Update(config ProxyWhitelistConfig) error { return p.update(config) }

func (p *proxyWhitelist) Contains(name string) bool               { return p.contains(name) }
func (p *proxyWhitelist) ObserveDNS(name string, answer *dns.Msg) { p.observeDNS(name, answer) }

func newProxyWhitelist(path string) (*proxyWhitelist, error) {
	p := &proxyWhitelist{path: path, setRun: runIPSet, iptRun: runIPTables, ipRun: runIPCommand, fullDevices: map[string]bool{}}
	if data, err := os.ReadFile(path + ".devices.json"); err == nil {
		if err := json.Unmarshal(data, &p.fullDevices); err != nil {
			return nil, err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	for mac, enabled := range p.fullDevices {
		parsed, err := net.ParseMAC(mac)
		if err != nil || parsed.String() != mac || !enabled {
			return nil, fmt.Errorf("invalid full-proxy device %q", mac)
		}
	}
	content, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		p.config.Domains = []string{}
		return p, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(content, &p.config); err != nil {
		return nil, err
	}
	config, err := normalizeProxyWhitelist(p.config)
	if err != nil {
		return nil, err
	}
	p.config = config
	return p, nil
}

func (p *proxyWhitelist) devices() map[string]bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	result := make(map[string]bool, len(p.fullDevices))
	for mac, enabled := range p.fullDevices {
		result[mac] = enabled
	}
	return result
}

func (p *proxyWhitelist) setDevice(mac string, enabled bool) error {
	parsed, err := net.ParseMAC(mac)
	if err != nil || len(parsed) != 6 {
		return errors.New("invalid MAC address")
	}
	mac = parsed.String()
	p.mu.Lock()
	defer p.mu.Unlock()
	old := p.fullDevices[mac]
	if enabled {
		p.fullDevices[mac] = true
	} else {
		delete(p.fullDevices, mac)
	}
	if p.active {
		if err := p.applyDeviceRules(); err != nil {
			if old {
				p.fullDevices[mac] = true
			} else {
				delete(p.fullDevices, mac)
			}
			_ = p.applyDeviceRules()
			return err
		}
	}
	data, err := json.MarshalIndent(p.fullDevices, "", "  ")
	if err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(p.path), "proxy-devices-*")
	if err != nil {
		return err
	}
	defer os.Remove(temp.Name())
	if err := temp.Chmod(0600); err != nil {
		temp.Close()
		return err
	}
	if _, err := temp.Write(data); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return os.Rename(temp.Name(), p.path+".devices.json")
}

func (p *proxyWhitelist) applyDeviceRules() error {
	if err := p.iptRun("-t", "nat", "-F", proxyNATChain); err != nil {
		return err
	}
	if err := p.iptRun("-t", "mangle", "-F", proxyUDPChain); err != nil {
		return err
	}
	for _, subnet := range []string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "127.0.0.0/8", "169.254.0.0/16", "224.0.0.0/4"} {
		if err := p.iptRun("-t", "nat", "-A", proxyNATChain, "-d", subnet, "-j", "RETURN"); err != nil {
			return err
		}
		if err := p.iptRun("-t", "mangle", "-A", proxyUDPChain, "-d", subnet, "-j", "RETURN"); err != nil {
			return err
		}
	}
	macs := make([]string, 0, len(p.fullDevices))
	for mac := range p.fullDevices {
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
	if err := p.iptRun("-t", "nat", "-A", proxyNATChain, "-i", p.lan, "-s", p.subnet, "-p", "tcp", "-m", "set", "--match-set", proxyIPSet, "dst", "-j", "REDIRECT", "--to-ports", "7893"); err != nil {
		return err
	}
	return p.iptRun("-t", "mangle", "-A", proxyUDPChain, "-i", p.lan, "-s", p.subnet, "-p", "udp", "-m", "set", "--match-set", proxyIPSet, "dst", "-j", "TPROXY", "--on-port", "7896", "--tproxy-mark", proxyUDPFwMark)
}

func normalizeProxyWhitelist(input proxyWhitelistConfig) (proxyWhitelistConfig, error) {
	if len(input.Domains) > 100 {
		return input, errors.New("白名单最多 100 个域名")
	}
	result := proxyWhitelistConfig{Group: strings.TrimSpace(input.Group), Domains: []string{}}
	for _, raw := range input.Domains {
		name := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(raw)), ".")
		if len(name) > 253 || !proxyDomainPattern.MatchString(name) {
			return result, fmt.Errorf("无效的域名 %q；请只填写域名，不含协议和路径", raw)
		}
		if !slices.Contains(result.Domains, name) {
			result.Domains = append(result.Domains, name)
		}
	}
	if len(result.Domains) > 0 && result.Group == "" {
		return result, errors.New("请选择代理组")
	}
	return result, nil
}

func (p *proxyWhitelist) get() proxyWhitelistConfig {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return proxyWhitelistConfig{Domains: append([]string{}, p.config.Domains...), Group: p.config.Group}
}

func (p *proxyWhitelist) save(config proxyWhitelistConfig) error {
	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(p.path), "proxy-whitelist-*")
	if err != nil {
		return err
	}
	defer os.Remove(temp.Name())
	if err := temp.Chmod(0600); err != nil {
		temp.Close()
		return err
	}
	if _, err := temp.Write(data); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return os.Rename(temp.Name(), p.path)
}

func (p *proxyWhitelist) update(config proxyWhitelistConfig) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.save(config); err != nil {
		return err
	}
	p.config = config
	if p.active {
		return p.setRun("flush", proxyIPSet)
	}
	return nil
}

func (p *proxyWhitelist) matches(name string) bool {
	for _, domain := range p.config.Domains {
		if name == domain || strings.HasSuffix(name, "."+domain) {
			return true
		}
	}
	return false
}

func (p *proxyWhitelist) contains(name string) bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.matches(name)
}

func (p *proxyWhitelist) observeDNS(name string, answer *dns.Msg) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if !p.active || !p.matches(name) || answer == nil || answer.Rcode != dns.RcodeSuccess {
		return
	}
	for _, rr := range answer.Answer {
		a, ok := rr.(*dns.A)
		if !ok {
			continue
		}
		ip, ok := netip.AddrFromSlice(a.A)
		if !ok || !ip.IsGlobalUnicast() || ip.IsPrivate() {
			continue
		}
		ttl := rr.Header().Ttl
		if ttl < 30 {
			ttl = 30
		}
		if ttl > 1800 {
			ttl = 1800
		}
		_ = p.setRun("add", proxyIPSet, ip.String(), "timeout", fmt.Sprint(ttl), "-exist")
	}
}

func (p *proxyWhitelist) apply(roles networkRoles, lan netip.Prefix) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.setRun("create", proxyIPSet, "hash:ip", "family", "inet", "timeout", "300", "-exist"); err != nil {
		return err
	}
	for _, item := range []struct{ table, chain string }{{"nat", proxyNATChain}, {"filter", proxyInputChain}, {"filter", proxyQUICChain}, {"mangle", proxyUDPChain}} {
		if err := p.iptRun("-t", item.table, "-S", item.chain); err != nil {
			if err := p.iptRun("-t", item.table, "-N", item.chain); err != nil {
				return err
			}
		}
		if err := p.iptRun("-t", item.table, "-F", item.chain); err != nil {
			return err
		}
	}
	subnet := lan.Masked().String()
	p.lan, p.wan, p.subnet = roles.LAN, roles.WAN, subnet
	if err := p.applyDeviceRules(); err != nil {
		return err
	}
	if err := p.iptRun("-t", "filter", "-A", proxyInputChain, "-i", roles.WAN, "-p", "tcp", "--dport", "7893", "-j", "DROP"); err != nil {
		return err
	}
	if err := p.iptRun("-t", "filter", "-A", proxyInputChain, "-i", roles.WAN, "-p", "tcp", "--dport", "7894", "-j", "DROP"); err != nil {
		return err
	}
	for _, port := range []string{"7895", "7896"} {
		if err := p.iptRun("-t", "filter", "-A", proxyInputChain, "-i", roles.WAN, "-p", "udp", "--dport", port, "-j", "DROP"); err != nil {
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
	for _, item := range []struct{ table, parent, child string }{{"nat", "PREROUTING", proxyNATChain}, {"filter", "INPUT", proxyInputChain}} {
		if err := p.iptRun("-t", item.table, "-C", item.parent, "-j", item.child); err != nil {
			if err := p.iptRun("-t", item.table, "-I", item.parent, "1", "-j", item.child); err != nil {
				return err
			}
		}
	}
	if err := p.iptRun("-t", "mangle", "-C", "PREROUTING", "-j", proxyUDPChain); err != nil {
		if err := p.iptRun("-t", "mangle", "-I", "PREROUTING", "1", "-j", proxyUDPChain); err != nil {
			return err
		}
	}
	if err := p.setRun("flush", proxyIPSet); err != nil {
		return err
	}
	p.lan, p.wan, p.subnet, p.active = roles.LAN, roles.WAN, subnet, true
	return nil
}

func (p *proxyWhitelist) close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.active {
		return nil
	}
	var errs []error
	for _, item := range []struct{ table, parent, child string }{{"nat", "PREROUTING", proxyNATChain}, {"filter", "INPUT", proxyInputChain}} {
		if err := p.iptRun("-t", item.table, "-C", item.parent, "-j", item.child); err == nil {
			errs = append(errs, p.iptRun("-t", item.table, "-D", item.parent, "-j", item.child))
		}
		errs = append(errs, p.iptRun("-t", item.table, "-F", item.child))
		errs = append(errs, p.iptRun("-t", item.table, "-X", item.child))
	}
	if err := p.iptRun("-t", "filter", "-C", forwardChain, "-j", proxyQUICChain); err == nil {
		errs = append(errs, p.iptRun("-t", "filter", "-D", forwardChain, "-j", proxyQUICChain))
	}
	whitelistQUIC := []string{"-t", "filter", "-C", forwardChain, "-i", p.lan, "-s", p.subnet, "-p", "udp", "--dport", "443", "-m", "set", "--match-set", proxyIPSet, "dst", "-j", "REJECT"}
	if err := p.iptRun(whitelistQUIC...); err == nil {
		whitelistQUIC[2] = "-D"
		errs = append(errs, p.iptRun(whitelistQUIC...))
	}
	errs = append(errs, p.iptRun("-t", "filter", "-F", proxyQUICChain), p.iptRun("-t", "filter", "-X", proxyQUICChain))
	if err := p.iptRun("-t", "mangle", "-C", "PREROUTING", "-j", proxyUDPChain); err == nil {
		errs = append(errs, p.iptRun("-t", "mangle", "-D", "PREROUTING", "-j", proxyUDPChain))
	}
	errs = append(errs, p.iptRun("-t", "mangle", "-F", proxyUDPChain), p.iptRun("-t", "mangle", "-X", proxyUDPChain))
	errs = append(errs, p.ipRun("rule", "del", "fwmark", proxyUDPFwMark, "lookup", proxyUDPTable, "priority", "1102"))
	errs = append(errs, p.ipRun("route", "del", "local", "0.0.0.0/0", "dev", "lo", "table", proxyUDPTable))
	errs = append(errs, p.setRun("destroy", proxyIPSet))
	p.active = false
	return errors.Join(errs...)
}

func runIPSet(args ...string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, "/usr/sbin/ipset", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("ipset %w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

func applyWhitelistRules(content []byte, config proxyWhitelistConfig) ([]byte, error) {
	var root map[string]any
	if err := yaml.Unmarshal(content, &root); err != nil {
		return nil, err
	}
	if root == nil {
		return nil, errors.New("Mihomo 配置为空")
	}
	groups, ok := root["proxy-groups"].([]any)
	if !ok {
		return nil, errors.New("订阅中没有代理组")
	}
	if len(config.Domains) > 0 {
		found := false
		for _, item := range groups {
			group, ok := item.(map[string]any)
			if ok && group["name"] == config.Group {
				found = true
				break
			}
		}
		if !found {
			return nil, fmt.Errorf("代理组 %q 不在当前订阅中", config.Group)
		}
	}
	rules := make([]string, 0, len(config.Domains)+1)
	for _, domain := range config.Domains {
		rules = append(rules, "DOMAIN-SUFFIX,"+domain+","+config.Group)
	}
	rules = append(rules, "MATCH,DIRECT")
	root["rules"] = rules
	root["mode"] = "rule"
	root["redir-port"] = 7893
	if config.Group != "" {
		root["listeners"] = []map[string]any{
			{"name": "pilot-full-device", "type": "redir", "port": 7894, "listen": "0.0.0.0", "proxy": config.Group},
			{"name": "pilot-full-device-udp", "type": "tproxy", "port": 7895, "listen": "0.0.0.0", "udp": true, "proxy": config.Group},
			{"name": "pilot-whitelist-udp", "type": "tproxy", "port": 7896, "listen": "0.0.0.0", "udp": true},
		}
	} else {
		delete(root, "listeners")
	}
	root["mixed-port"] = 0
	root["allow-lan"] = true
	root["bind-address"] = "*"
	dnsUpstream := "https://1.1.1.1/dns-query"
	if config.Group != "" {
		dnsUpstream += "#" + config.Group
	}
	root["dns"] = map[string]any{
		"enable":                  true,
		"listen":                  "127.0.0.1:1053",
		"ipv6":                    false,
		"enhanced-mode":           "fake-ip",
		"fake-ip-range":           "198.18.0.1/16",
		"default-nameserver":      []string{"223.5.5.5"},
		"proxy-server-nameserver": []string{"223.5.5.5"},
		"nameserver":              []string{dnsUpstream},
	}
	root["sniffer"] = map[string]any{"enable": true, "force-dns-mapping": true, "override-destination": true, "sniff": map[string]any{"HTTP": map[string]any{"ports": []int{80, 8080, 8880}}, "TLS": map[string]any{"ports": []int{443, 8443}}}}
	return yaml.Marshal(root)
}
