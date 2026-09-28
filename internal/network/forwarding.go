package network

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

const (
	forwardChain = "PILOT_FWD"
	natChain     = "PILOT_NAT"
)

type forwarder struct {
	mu             sync.Mutex
	run            func(...string) error
	setIPForward   func(bool) error
	initialForward bool
	active         bool
	proxy          *proxyWhitelist
}

type Forwarder = forwarder

func NewForwarder() (*Forwarder, error)                        { return newForwarder() }
func (f *forwarder) SetProxy(proxy *ProxyWhitelist)            { f.proxy = proxy }
func (f *forwarder) Apply(roles Roles, lan netip.Prefix) error { return f.apply(roles, lan) }
func (f *forwarder) Close() error                              { return f.close() }

func newForwarder() (*forwarder, error) {
	value, err := os.ReadFile("/proc/sys/net/ipv4/ip_forward")
	if err != nil {
		return nil, err
	}
	return &forwarder{
		run:            runIPTables,
		setIPForward:   setIPForward,
		initialForward: strings.TrimSpace(string(value)) == "1",
	}, nil
}

func (f *forwarder) apply(roles networkRoles, lan netip.Prefix) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if roles.WAN == roles.LAN || !interfaceNamePattern.MatchString(roles.WAN) || !interfaceNamePattern.MatchString(roles.LAN) || !lan.Addr().Is4() {
		return errors.New("转发网卡或 LAN 网段无效")
	}
	f.active = true
	subnet := lan.Masked().String()
	for _, chain := range []struct{ table, name string }{{"filter", forwardChain}, {"nat", natChain}} {
		if err := f.run("-t", chain.table, "-S", chain.name); err != nil {
			if err := f.run("-t", chain.table, "-N", chain.name); err != nil {
				return fmt.Errorf("创建 iptables 链 %s: %w", chain.name, err)
			}
		}
		if err := f.run("-t", chain.table, "-F", chain.name); err != nil {
			return err
		}
	}
	rules := [][]string{
		{"-t", "filter", "-A", forwardChain, "-i", roles.WAN, "-o", roles.LAN, "-m", "conntrack", "--ctstate", "ESTABLISHED,RELATED", "-j", "ACCEPT"},
		{"-t", "filter", "-A", forwardChain, "-i", roles.WAN, "-o", roles.LAN, "-j", "DROP"},
		{"-t", "filter", "-A", forwardChain, "-i", roles.LAN, "-o", roles.WAN, "-s", subnet, "-j", "ACCEPT"},
		{"-t", "nat", "-A", natChain, "-s", subnet, "-o", roles.WAN, "-j", "MASQUERADE"},
	}
	for _, rule := range rules {
		if err := f.run(rule...); err != nil {
			return fmt.Errorf("设置转发规则: %w", err)
		}
	}
	for _, jump := range []struct{ table, parent, child string }{{"filter", "FORWARD", forwardChain}, {"nat", "POSTROUTING", natChain}} {
		if err := f.run("-t", jump.table, "-C", jump.parent, "-j", jump.child); err != nil {
			if err := f.run("-t", jump.table, "-I", jump.parent, "1", "-j", jump.child); err != nil {
				return fmt.Errorf("启用转发规则: %w", err)
			}
		}
	}
	if err := f.setIPForward(true); err != nil {
		return fmt.Errorf("启用 IPv4 转发: %w", err)
	}
	if f.proxy != nil {
		if err := f.proxy.apply(roles, lan); err != nil {
			return fmt.Errorf("启用代理白名单: %w", err)
		}
	}
	f.active = true
	return nil
}

func (f *forwarder) close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.active {
		return nil
	}
	var errs []error
	if f.proxy != nil {
		errs = append(errs, f.proxy.close())
	}
	if !f.initialForward {
		errs = append(errs, f.setIPForward(false))
	}
	for _, jump := range []struct{ table, parent, child string }{{"filter", "FORWARD", forwardChain}, {"nat", "POSTROUTING", natChain}} {
		if err := f.run("-t", jump.table, "-C", jump.parent, "-j", jump.child); err == nil {
			errs = append(errs, f.run("-t", jump.table, "-D", jump.parent, "-j", jump.child))
		}
		errs = append(errs, f.run("-t", jump.table, "-F", jump.child))
		errs = append(errs, f.run("-t", jump.table, "-X", jump.child))
	}
	f.active = false
	return errors.Join(errs...)
}

func runIPTables(args ...string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "/usr/sbin/iptables", append([]string{"-w", "3"}, args...)...)
	output, err := command.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

func setIPForward(enabled bool) error {
	value := []byte("0\n")
	if enabled {
		value = []byte("1\n")
	}
	return os.WriteFile("/proc/sys/net/ipv4/ip_forward", value, 0644)
}
