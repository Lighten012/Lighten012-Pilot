package firewall

import (
	"encoding/hex"
	"fmt"
	"net"
	"net/netip"
	"os"
	"strconv"
	"strings"
)

func socketAddress(raw string) (netip.Addr, int, error) {
	p := strings.Split(raw, ":")
	if len(p) != 2 {
		return netip.Addr{}, 0, fmt.Errorf("socket address")
	}
	b, e := hex.DecodeString(p[0])
	if e != nil || len(b) != 4 && len(b) != 16 {
		return netip.Addr{}, 0, fmt.Errorf("socket address")
	}
	// Linux /proc stores each 32-bit address word in host byte order (amd64/arm64).
	for i := 0; i < len(b); i += 4 {
		b[i], b[i+3] = b[i+3], b[i]
		b[i+1], b[i+2] = b[i+2], b[i+1]
	}
	a, _ := netip.AddrFromSlice(b)
	port, e := strconv.ParseUint(p[1], 16, 16)
	return a.Unmap(), int(port), e
}
func listenerConflict(table, protocol string, wan map[netip.Addr]bool, forwards []Forward) error {
	for _, line := range strings.Split(table, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 4 || protocol == "tcp" && fields[3] != "0A" {
			continue
		}
		a, port, e := socketAddress(fields[1])
		if e != nil || !a.IsUnspecified() && !wan[a] {
			continue
		}
		for _, f := range forwards {
			if f.Enabled && f.Protocol == protocol && f.ExternalPort == port {
				return fmt.Errorf("%s/%d 已被路由器本机服务监听，请换一个外部端口", protocol, port)
			}
		}
	}
	return nil
}
func checkListeners(c Config) error {
	if !c.Enabled || len(c.Forwards) == 0 {
		return nil
	}
	iface, e := net.InterfaceByName(c.WAN)
	if e != nil {
		return e
	}
	addrs, e := iface.Addrs()
	if e != nil {
		return e
	}
	wan := map[netip.Addr]bool{}
	for _, address := range addrs {
		p, e := netip.ParsePrefix(address.String())
		if e == nil {
			wan[p.Addr().Unmap()] = true
		}
	}
	for _, name := range []string{"tcp", "tcp6", "udp", "udp6"} {
		data, e := os.ReadFile("/proc/net/" + name)
		if os.IsNotExist(e) {
			continue
		}
		if e != nil {
			return e
		}
		if e = listenerConflict(string(data), strings.TrimSuffix(name, "6"), wan, c.Forwards); e != nil {
			return e
		}
	}
	return nil
}
