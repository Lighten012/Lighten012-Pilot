package network

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/netip"
	"regexp"
	"strings"
)

type Port struct {
	Interface string `json:"interface"`
	Mode      string `json:"mode"`
	Address   string `json:"address"`
	Gateway   string `json:"gateway"`
}
type Config struct {
	WAN Port `json:"wan"`
	LAN Port `json:"lan"`
}
type Device struct {
	Name      string   `json:"name"`
	Addresses []string `json:"addresses"`
	State     string   `json:"state"`
	Protected bool     `json:"protected"`
	Up        bool     `json:"up"`
	Current   *Port    `json:"current"`
	Reason    string   `json:"reason"`
}
type Inventory struct {
	Devices  []Device `json:"devices"`
	Routes   []Route  `json:"routes"`
	Revision string   `json:"revision"`
	Backend  string   `json:"backend"`
	Blocked  string   `json:"blocked"`
}
type Route struct {
	Dst     string `json:"dst"`
	Dev     string `json:"dev"`
	Gateway string `json:"gateway"`
}
type Change struct {
	Interface string `json:"interface"`
	Before    *Port  `json:"before"`
	After     Port   `json:"after"`
	Changed   bool   `json:"changed"`
	File      string `json:"file"`
	WasUp     bool   `json:"wasUp"`
}
type Plan struct {
	Config   Config   `json:"config"`
	Revision string   `json:"revision"`
	Token    string   `json:"token"`
	Changes  []Change `json:"changes"`
	Warnings []string `json:"warnings"`
}

var interfacePattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,14}$`)

func address(s string) (netip.Prefix, error) {
	p, e := netip.ParsePrefix(s)
	if e != nil || !p.Addr().Is4() || p.Bits() < 1 || p.Bits() > 30 {
		return netip.Prefix{}, fmt.Errorf("静态地址须为 IPv4/CIDR，前缀范围 /1 至 /30")
	}
	a := p.Addr()
	if !a.IsGlobalUnicast() || a.IsLoopback() || a.IsLinkLocalUnicast() {
		return p, fmt.Errorf("不支持该 IPv4 地址")
	}
	b := a.As4()
	host := uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3])
	mask := uint32(0xffffffff) << uint(32-p.Bits())
	if host & ^mask == 0 || host & ^mask == ^mask {
		return p, fmt.Errorf("不能使用网络地址或广播地址")
	}
	return p, nil
}
func overlap(a, b netip.Prefix) bool { return a.Contains(b.Addr()) || b.Contains(a.Addr()) }
func Validate(c Config, inv Inventory) error {
	if inv.Blocked != "" {
		return fmt.Errorf("%s", inv.Blocked)
	}
	if c.WAN.Interface == c.LAN.Interface {
		return fmt.Errorf("WAN 和 LAN 必须使用不同网卡")
	}
	devices := map[string]Device{}
	for _, d := range inv.Devices {
		devices[d.Name] = d
	}
	for role, p := range map[string]Port{"WAN": c.WAN, "LAN": c.LAN} {
		if !interfacePattern.MatchString(p.Interface) || p.Interface == "lo" {
			return fmt.Errorf("%s 网卡名称无效", role)
		}
		d, ok := devices[p.Interface]
		if !ok {
			return fmt.Errorf("网卡 %s 不存在", p.Interface)
		}
		if d.Reason != "" {
			return fmt.Errorf("%s: %s", p.Interface, d.Reason)
		}
		if p.Mode != "dhcp" && p.Mode != "static" {
			return fmt.Errorf("不支持的地址方式")
		}
		if role == "LAN" && p.Mode != "static" {
			return fmt.Errorf("LAN 必须使用静态 IPv4")
		}
		if p.Mode == "dhcp" {
			if p.Address != "" || p.Gateway != "" {
				return fmt.Errorf("DHCP 模式不能同时填写静态地址或网关")
			}
		} else {
			prefix, e := address(p.Address)
			if e != nil {
				return fmt.Errorf("%s: %w", role, e)
			}
			if role == "LAN" && p.Gateway != "" {
				return fmt.Errorf("LAN 不设置默认网关")
			}
			if role == "WAN" {
				g, e := netip.ParseAddr(p.Gateway)
				if e != nil || !g.Is4() || !prefix.Contains(g) || g == prefix.Addr() {
					return fmt.Errorf("WAN 网关必须是同网段的另一个 IPv4 地址")
				}
				if _, e = address(g.String() + fmt.Sprintf("/%d", prefix.Bits())); e != nil {
					return fmt.Errorf("WAN 网关不能是网络或广播地址")
				}
			}
		}
		if d.Protected && (d.Current == nil || *d.Current != p) {
			return fmt.Errorf("%s 是受保护的管理接口，只能保留当前配置", p.Interface)
		}
	}
	lan, _ := address(c.LAN.Address)
	if c.WAN.Mode == "static" {
		wan, _ := address(c.WAN.Address)
		if overlap(wan, lan) {
			return fmt.Errorf("WAN 和 LAN 网段不能重叠")
		}
	} else {
		for _, a := range devices[c.WAN.Interface].Addresses {
			p, e := netip.ParsePrefix(a)
			if e == nil && p.Addr().Is4() && overlap(lan, p) {
				return fmt.Errorf("LAN 与 WAN 当前租约网段重叠")
			}
		}
	}
	for _, r := range inv.Routes {
		if r.Dst == "default" || r.Dev == c.LAN.Interface {
			continue
		}
		p, e := netip.ParsePrefix(r.Dst)
		if e == nil && p.Addr().Is4() && overlap(lan, p) {
			return fmt.Errorf("LAN 与现有路由 %s (%s) 冲突", r.Dst, r.Dev)
		}
	}
	return nil
}
func Fingerprint(v any) string {
	b, _ := json.Marshal(v)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
func Render(p Port) string {
	s := fmt.Sprintf("auto %s\niface %s inet %s\n", p.Interface, p.Interface, p.Mode)
	if p.Mode == "static" {
		s += "    address " + p.Address + "\n"
		if p.Gateway != "" {
			s += "    gateway " + p.Gateway + "\n"
		}
	}
	return s
}
func ManagedText(p Port) string {
	return "# Managed by Lighten012-Pilot. Edit using the control panel.\n" + Render(p)
}
func Canonical(c Config) Config {
	for _, p := range []*Port{&c.WAN, &c.LAN} {
		p.Interface = strings.TrimSpace(p.Interface)
		p.Address = strings.TrimSpace(p.Address)
		p.Gateway = strings.TrimSpace(p.Gateway)
	}
	return c
}
