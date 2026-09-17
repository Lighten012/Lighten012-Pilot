package firewall

import (
	"fmt"
	"github.com/Lighten012/Lighten012-Pilot/internal/network"
	"net/netip"
	"regexp"
	"strconv"
	"strings"
)

type Rule struct {
	Enabled     bool   `json:"enabled"`
	Action      string `json:"action"`
	Protocol    string `json:"protocol"`
	Source      string `json:"source"`
	Destination string `json:"destination"`
	Port        string `json:"port"`
}
type Config struct {
	Enabled       bool   `json:"enabled"`
	NAT           bool   `json:"nat"`
	DefaultAction string `json:"defaultAction"`
	WAN           string `json:"wan"`
	LAN           string `json:"lan"`
	Subnet        string `json:"subnet"`
	Rules         []Rule `json:"rules"`
}

func Default() Config { return Config{DefaultAction: "ACCEPT", Rules: []Rule{}} }

var iface = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,14}$`)

func prefix(s string) (string, error) {
	if s == "" {
		return "", nil
	}
	if a, e := netip.ParseAddr(s); e == nil && a.Is4() {
		return a.String() + "/32", nil
	}
	p, e := netip.ParsePrefix(s)
	if e != nil || !p.Addr().Is4() {
		return "", fmt.Errorf("地址须为 IPv4 或 IPv4/CIDR")
	}
	return p.Masked().String(), nil
}
func Normalize(c Config) (Config, error) {
	c.Rules = append([]Rule{}, c.Rules...)
	if c.DefaultAction != "ACCEPT" && c.DefaultAction != "DROP" {
		return c, fmt.Errorf("默认策略只能是放行或阻止")
	}
	if c.NAT && !c.Enabled {
		return c, fmt.Errorf("NAT 需要先启用转发防火墙")
	}
	if len(c.Rules) > 128 {
		return c, fmt.Errorf("最多 128 条规则")
	}
	for i := range c.Rules {
		r := &c.Rules[i]
		if r.Action != "ACCEPT" && r.Action != "DROP" {
			return c, fmt.Errorf("规则 %d 动作无效", i+1)
		}
		if r.Protocol != "all" && r.Protocol != "tcp" && r.Protocol != "udp" && r.Protocol != "icmp" {
			return c, fmt.Errorf("规则 %d 协议无效", i+1)
		}
		var e error
		r.Source, e = prefix(strings.TrimSpace(r.Source))
		if e != nil {
			return c, e
		}
		r.Destination, e = prefix(strings.TrimSpace(r.Destination))
		if e != nil {
			return c, e
		}
		r.Port = strings.TrimSpace(r.Port)
		if r.Port != "" {
			if r.Protocol != "tcp" && r.Protocol != "udp" {
				return c, fmt.Errorf("只有 TCP/UDP 能指定目标端口")
			}
			parts := strings.Split(r.Port, "-")
			if len(parts) > 2 {
				return c, fmt.Errorf("端口格式为 80 或 8000-8010")
			}
			prev := 0
			for _, part := range parts {
				n, e := strconv.Atoi(part)
				if e != nil || n < 1 || n > 65535 || n < prev {
					return c, fmt.Errorf("端口须在 1–65535 且范围递增")
				}
				prev = n
			}
		}
	}
	return c, nil
}
func Bind(c Config, s network.Status) (Config, error) {
	if s.Pending != nil {
		return c, fmt.Errorf("请先完成 WAN/LAN 配置确认")
	}
	if !c.Enabled {
		return c, nil
	}
	if s.Saved == nil {
		return c, fmt.Errorf("请先保存 WAN/LAN 配置")
	}
	w, l := s.Saved.WAN, s.Saved.LAN
	if !iface.MatchString(w.Interface) || !iface.MatchString(l.Interface) || w.Interface == l.Interface || w.Interface == "lo" || l.Interface == "lo" {
		return c, fmt.Errorf("WAN/LAN 接口无效")
	}
	p, e := netip.ParsePrefix(l.Address)
	if e != nil || !p.Addr().Is4() || l.Mode != "static" {
		return c, fmt.Errorf("LAN 需要静态 IPv4")
	}
	foundWAN, foundLAN := false, false
	for _, d := range s.Inventory.Devices {
		if d.Name == w.Interface {
			foundWAN = true
		}
		if d.Name == l.Interface {
			for _, a := range d.Addresses {
				if a == l.Address {
					foundLAN = true
				}
			}
		}
	}
	if !foundWAN || !foundLAN {
		return c, fmt.Errorf("WAN 网卡或 LAN 地址与已保存配置不一致")
	}
	c.WAN = w.Interface
	c.LAN = l.Interface
	c.Subnet = p.Masked().String()
	return c, nil
}

// Only Pilot-owned chains are replaced; INPUT/OUTPUT and other chains are untouched.
func Render(c Config) string {
	var b strings.Builder
	b.WriteString("*filter\n:PILOT_FWD - [0:0]\n")
	if c.Enabled {
		outbound := "-A PILOT_FWD -i " + c.LAN + " -o " + c.WAN
		inbound := "-A PILOT_FWD -i " + c.WAN + " -o " + c.LAN
		b.WriteString(outbound + " ! -s " + c.Subnet + " -j DROP\n")
		b.WriteString(outbound + " -m conntrack --ctstate INVALID -j DROP\n")
		b.WriteString(inbound + " -m conntrack --ctstate INVALID -j DROP\n")
		b.WriteString(outbound + " -m conntrack --ctstate ESTABLISHED,RELATED -j ACCEPT\n")
		b.WriteString(inbound + " -d " + c.Subnet + " -m conntrack --ctstate ESTABLISHED,RELATED -j ACCEPT\n")
		for _, r := range c.Rules {
			if !r.Enabled {
				continue
			}
			s := outbound
			if r.Protocol != "all" {
				s += " -p " + r.Protocol
			}
			if r.Source != "" {
				s += " -s " + r.Source
			}
			if r.Destination != "" {
				s += " -d " + r.Destination
			}
			if r.Port != "" {
				s += " --dport " + strings.ReplaceAll(r.Port, "-", ":")
			}
			b.WriteString(s + " -j " + r.Action + "\n")
		}
		b.WriteString(outbound + " -j " + c.DefaultAction + "\n" + inbound + " -j DROP\n")
		// No forwarding between LAN and other interfaces while Pilot owns the LAN boundary.
		b.WriteString("-A PILOT_FWD -i " + c.LAN + " -j DROP\n-A PILOT_FWD -o " + c.LAN + " -j DROP\n")
	}
	b.WriteString("-A PILOT_FWD -j RETURN\nCOMMIT\n*nat\n:PILOT_NAT - [0:0]\n")
	if c.Enabled && c.NAT {
		b.WriteString("-A PILOT_NAT -s " + c.Subnet + " -o " + c.WAN + " -j MASQUERADE\n")
	}
	b.WriteString("-A PILOT_NAT -j RETURN\nCOMMIT\n")
	return b.String()
}
