package services

import (
	"fmt"
	"net"
	"net/netip"
	"regexp"
	"strings"

	"github.com/Lighten012/Lighten012-Pilot/internal/network"
)

type Record struct {
	Name  string `json:"name"`
	Type  string `json:"type"`
	Value string `json:"value"`
	TTL   uint32 `json:"ttl"`
}
type DNSConfig struct {
	Enabled  bool     `json:"enabled"`
	Upstream string   `json:"upstream"`
	Records  []Record `json:"records"`
}
type DHCPConfig struct {
	Enabled      bool   `json:"enabled"`
	Start        string `json:"start"`
	End          string `json:"end"`
	LeaseMinutes int    `json:"leaseMinutes"`
}
type Config struct {
	Interface string     `json:"interface"`
	Address   string     `json:"address"`
	DNS       DNSConfig  `json:"dns"`
	DHCP      DHCPConfig `json:"dhcp"`
}

func (c Config) Enabled() bool { return c.DNS.Enabled || c.DHCP.Enabled }
func Default() Config {
	return Config{DNS: DNSConfig{Upstream: "119.29.29.29", Records: []Record{}}, DHCP: DHCPConfig{LeaseMinutes: 720}}
}

var labelPattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)

func Name(s string) (string, error) {
	s = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(s)), ".")
	if len(s) == 0 || len(s) > 253 {
		return "", fmt.Errorf("域名长度应为 1–253 个字符")
	}
	for _, label := range strings.Split(s, ".") {
		if !labelPattern.MatchString(label) {
			return "", fmt.Errorf("域名只支持英文字母、数字与连字符；不支持通配符，请将国际化域名转换为 Punycode")
		}
	}
	return s + ".", nil
}
func Normalize(c Config) (Config, error) {
	c.Interface = strings.TrimSpace(c.Interface)
	c.Address = strings.TrimSpace(c.Address)
	c.DNS.Upstream = strings.TrimSpace(c.DNS.Upstream)
	a, e := netip.ParseAddr(c.DNS.Upstream)
	if e != nil || !a.Is4() || !a.IsGlobalUnicast() || a.IsLoopback() || a.IsLinkLocalUnicast() {
		return c, fmt.Errorf("上游 DNS 必须是可用的 IPv4 地址，使用标准 53 端口")
	}
	c.DNS.Upstream = a.String()
	if len(c.DNS.Records) > 128 {
		return c, fmt.Errorf("最多支持 128 条本地记录")
	}
	if c.DNS.Records == nil {
		c.DNS.Records = []Record{}
	}
	seen := map[string]bool{}
	for i, r := range c.DNS.Records {
		r.Name, e = Name(r.Name)
		if e != nil {
			return c, fmt.Errorf("第 %d 条记录: %w", i+1, e)
		}
		r.Type = strings.ToUpper(strings.TrimSpace(r.Type))
		a, e = netip.ParseAddr(strings.TrimSpace(r.Value))
		if e != nil || (r.Type != "A" && r.Type != "AAAA") || (r.Type == "A" && !a.Is4()) || (r.Type == "AAAA" && (!a.Is6() || a.Is4In6())) {
			return c, fmt.Errorf("第 %d 条记录的 A/AAAA 类型与地址不匹配", i+1)
		}
		if r.TTL < 1 || r.TTL > 86400 {
			return c, fmt.Errorf("TTL 应在 1–86400 秒之间")
		}
		r.Value = a.String()
		key := r.Name + "/" + r.Type + "/" + r.Value
		if seen[key] {
			return c, fmt.Errorf("存在重复解析记录")
		}
		seen[key] = true
		c.DNS.Records[i] = r
	}
	c.DHCP.Start = strings.TrimSpace(c.DHCP.Start)
	c.DHCP.End = strings.TrimSpace(c.DHCP.End)
	if c.DHCP.Enabled && !c.DNS.Enabled {
		return c, fmt.Errorf("DHCP 下发 Pilot DNS，请先启用 DNS")
	}
	if c.Enabled() {
		if e = ValidatePool(c); e != nil {
			return c, e
		}
	}
	return c, nil
}
func ValidatePool(c Config) error {
	p, e := netip.ParsePrefix(c.Address)
	if e != nil || !p.Addr().Is4() || p.Bits() < 1 || p.Bits() > 30 {
		return fmt.Errorf("需要已确认的 LAN IPv4/CIDR 地址")
	}
	if !c.DHCP.Enabled {
		return nil
	}
	a, e1 := netip.ParseAddr(c.DHCP.Start)
	b, e2 := netip.ParseAddr(c.DHCP.End)
	if e1 != nil || e2 != nil || !a.Is4() || !b.Is4() || !p.Contains(a) || !p.Contains(b) || a.Compare(b) > 0 {
		return fmt.Errorf("地址池必须位于 LAN 网段内，起始地址不能大于结束地址")
	}
	n := p.Masked().Addr().As4()
	mask := net.CIDRMask(p.Bits(), 32)
	last := [4]byte{}
	for i := range n {
		last[i] = n[i] | ^mask[i]
	}
	if a == p.Masked().Addr() || b == netip.AddrFrom4(last) || (a.Compare(p.Addr()) <= 0 && b.Compare(p.Addr()) >= 0) {
		return fmt.Errorf("地址池不能包含网络地址、广播地址或 Pilot LAN 地址")
	}
	if c.DHCP.LeaseMinutes < 2 || c.DHCP.LeaseMinutes > 10080 {
		return fmt.Errorf("租期范围为 2–10080 分钟")
	}
	return nil
}
func ValidateLAN(c Config, s network.Status) error {
	if !c.Enabled() {
		return nil
	}
	if s.Pending != nil {
		return fmt.Errorf("请先确认或恢复 WAN/LAN 变更")
	}
	if s.Saved == nil || s.Saved.LAN.Mode != "static" || c.Interface != s.Saved.LAN.Interface || c.Address != s.Saved.LAN.Address {
		return fmt.Errorf("服务必须绑定到已确认的 LAN 接口和地址，请刷新页面")
	}
	if s.Inventory.Blocked != "" {
		return fmt.Errorf("网络状态不可用: %s", s.Inventory.Blocked)
	}
	found := false
	for _, d := range s.Inventory.Devices {
		for _, ip := range d.Addresses {
			p, e := netip.ParsePrefix(ip)
			if e == nil && p.Addr().String() == c.DNS.Upstream {
				return fmt.Errorf("上游 DNS 不能指向本机地址，避免转发循环")
			}
		}
		if d.Name == c.Interface {
			if d.Protected || d.Reason != "" {
				return fmt.Errorf("不能在受保护或不受支持的接口上提供 LAN 服务")
			}
			for _, a := range d.Addresses {
				if a == c.Address {
					found = true
				}
			}
		}
	}
	if !found {
		return fmt.Errorf("LAN 地址尚未实际生效")
	}
	return nil
}
