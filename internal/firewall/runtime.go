package firewall

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

type Backend interface {
	Available() error
	Forwarding() (string, error)
	Apply(Config, string) error
	Check(Config) error
}
type Linux struct{}

func command(input, name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	if input != "" {
		cmd.Stdin = strings.NewReader(input)
	}
	b, e := cmd.CombinedOutput()
	if e != nil {
		return "", fmt.Errorf("%s: %w: %s", name, e, strings.TrimSpace(string(b)))
	}
	return string(b), nil
}
func (*Linux) Available() error {
	for _, n := range []string{"iptables", "iptables-restore"} {
		if _, e := exec.LookPath(n); e != nil {
			return fmt.Errorf("请安装 iptables: %w", e)
		}
	}
	return nil
}
func (*Linux) Forwarding() (string, error) {
	b, e := os.ReadFile("/proc/sys/net/ipv4/ip_forward")
	return strings.TrimSpace(string(b)), e
}
func (*Linux) Apply(c Config, baseline string) error {
	if _, e := command(Render(c), "iptables-restore", "--wait", "5", "--noflush"); e != nil {
		return e
	}
	for _, h := range [][3]string{{"filter", "FORWARD", "PILOT_FWD"}, {"nat", "POSTROUTING", "PILOT_NAT"}} {
		if _, e := command("", "iptables", "-w", "5", "-t", h[0], "-C", h[1], "-j", h[2]); e != nil {
			if _, e = command("", "iptables", "-w", "5", "-t", h[0], "-I", h[1], "1", "-j", h[2]); e != nil {
				return e
			}
		}
	}
	value := baseline
	if c.Enabled {
		value = "1"
	}
	if value != "0" && value != "1" {
		return fmt.Errorf("IPv4 转发恢复值无效")
	}
	current, e := (&Linux{}).Forwarding()
	if e != nil {
		return e
	}
	if current == value {
		return nil
	}
	return os.WriteFile("/proc/sys/net/ipv4/ip_forward", []byte(value+"\n"), 0644)
}
func (*Linux) Check(c Config) error {
	_, e := command(Render(c), "iptables-restore", "--wait", "5", "--noflush", "--test")
	return e
}
