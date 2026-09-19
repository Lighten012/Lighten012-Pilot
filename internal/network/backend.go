package network

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

type File struct {
	Path    string `json:"path"`
	Exists  bool   `json:"exists"`
	Content string `json:"content"`
	Mode    uint32 `json:"mode"`
}
type Backend interface {
	Inspect() (Inventory, []File, error)
	Prepare(Config, Inventory, []File) (Plan, []File, error)
	Activate([]Change, bool) error
	Write([]File) error
}
type Linux struct {
	Root      string
	StateDir  string
	Protected map[string]bool
}

func run(name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	out, e := exec.CommandContext(ctx, name, args...).CombinedOutput()
	if e != nil {
		return nil, fmt.Errorf("%s 执行失败: %s", filepath.Base(name), strings.TrimSpace(string(out)))
	}
	return out, nil
}
func ReadFile(p string) (File, error) {
	b, e := os.ReadFile(p)
	if os.IsNotExist(e) {
		return File{Path: p, Mode: 0644}, nil
	}
	if e != nil {
		return File{}, e
	}
	st, e := os.Stat(p)
	if e != nil {
		return File{}, e
	}
	return File{Path: p, Exists: true, Content: string(b), Mode: uint32(st.Mode().Perm())}, nil
}

// parseStanza only accepts simple IPv4 configurations. Unknown directives are
// rejected rather than dropped. IPv6 stanzas and unrelated text are preserved.
func parseStanza(text, name string) (*Port, int, int, error) {
	lines := strings.Split(text, "\n")
	start, end := -1, -1
	var p *Port
	for i, line := range lines {
		f := strings.Fields(line)
		if len(f) >= 3 && f[0] == "iface" && f[1] == name && f[2] == "inet" {
			if p != nil {
				return nil, 0, 0, fmt.Errorf("存在重复 IPv4 配置")
			}
			if len(f) != 4 || (f[3] != "static" && f[3] != "dhcp") {
				return nil, 0, 0, fmt.Errorf("仅支持简单 static / dhcp 配置")
			}
			p = &Port{Interface: name, Mode: f[3]}
			start = i
			end = i + 1
			for j := i + 1; j < len(lines); j++ {
				v := strings.Fields(lines[j])
				if len(v) > 0 && !strings.HasPrefix(v[0], "#") && isTop(v[0]) {
					break
				}
				end = j + 1
				if len(v) == 0 || strings.HasPrefix(v[0], "#") {
					continue
				}
				if len(v) != 2 {
					return nil, 0, 0, fmt.Errorf("配置含有暂不支持的选项")
				}
				switch v[0] {
				case "address":
					p.Address = v[1]
				case "gateway":
					p.Gateway = v[1]
				case "netmask":
					return nil, 0, 0, fmt.Errorf("请先将 netmask 配置规范为 address CIDR 后接管")
				default:
					return nil, 0, 0, fmt.Errorf("暂不接管包含 %s 的配置", v[0])
				}
			}
		}
	}
	if p != nil && p.Mode == "static" {
		if _, e := address(p.Address); e != nil {
			return nil, 0, 0, e
		}
	}
	return p, start, end, nil
}
func isTop(s string) bool {
	return s == "iface" || s == "auto" || strings.HasPrefix(s, "allow-") || s == "source" || s == "source-directory" || s == "mapping"
}
func (l *Linux) files() ([]File, error) {
	main, e := ReadFile(filepath.Join(l.Root, "interfaces"))
	if e != nil {
		return nil, e
	}
	files := []File{main}
	if !strings.Contains(main.Content, "source /etc/network/interfaces.d/*") {
		return nil, fmt.Errorf("当前仅支持标准 source /etc/network/interfaces.d/* 布局")
	}
	entries, e := os.ReadDir(filepath.Join(l.Root, "interfaces.d"))
	if e != nil {
		return nil, e
	}
	for _, en := range entries {
		if en.IsDir() {
			continue
		}
		p := filepath.Join(l.Root, "interfaces.d", en.Name())
		if en.Type()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("配置目录包含符号链接，暂不接管")
		}
		f, e := ReadFile(p)
		if e != nil {
			return nil, e
		}
		files = append(files, f)
	}
	for _, f := range files {
		for _, line := range strings.Split(f.Content, "\n") {
			v := strings.Fields(line)
			if len(v) == 0 || strings.HasPrefix(v[0], "#") {
				continue
			}
			if v[0] == "mapping" || v[0] == "source-directory" || (v[0] == "source" && (f.Path != main.Path || strings.TrimSpace(line) != "source /etc/network/interfaces.d/*")) {
				return nil, fmt.Errorf("包含复杂 source/mapping 配置，暂不接管")
			}
		}
	}
	return files, nil
}
func (l *Linux) Inspect() (Inventory, []File, error) {
	inv := Inventory{Devices: []Device{}, Routes: []Route{}, Backend: "ifupdown"}
	for _, svc := range []string{"NetworkManager", "systemd-networkd"} {
		if b, _ := run("/usr/bin/systemctl", "is-active", svc); strings.TrimSpace(string(b)) == "active" {
			inv.Blocked = "检测到另一网络管理器运行，请先统一管理方式"
		}
	}
	files, e := l.files()
	if e != nil {
		inv.Blocked = e.Error()
	}
	devs, e := net.Interfaces()
	if e != nil {
		return inv, nil, e
	}
	for _, n := range devs {
		if n.Flags&net.FlagLoopback != 0 {
			continue
		}
		d := Device{Name: n.Name, Up: n.Flags&net.FlagUp != 0, Addresses: []string{}, Protected: l.Protected[n.Name]}
		a, _ := n.Addrs()
		for _, v := range a {
			d.Addresses = append(d.Addresses, v.String())
		}
		b, _ := os.ReadFile("/sys/class/net/" + n.Name + "/operstate")
		d.State = strings.TrimSpace(string(b))
		if _, e := os.Stat("/sys/class/net/" + n.Name + "/master"); e == nil {
			d.Reason = "桥接或聚合从接口暂不支持"
		}
		for _, f := range files {
			p, _, _, er := parseStanza(f.Content, n.Name)
			if er != nil {
				d.Reason = er.Error()
			}
			if p != nil {
				if d.Current != nil {
					d.Reason = "多文件重复接口配置"
				}
				d.Current = p
			}
		}
		if d.Current == nil && len(d.Addresses) > 0 {
			for _, a := range d.Addresses {
				if !strings.Contains(a, ":") {
					d.Reason = "接口已有未被 ifupdown 管理的 IPv4 地址"
				}
			}
		}
		inv.Devices = append(inv.Devices, d)
	}
	b, e := run("/usr/sbin/ip", "-j", "-4", "route", "show", "table", "main")
	if e != nil {
		return inv, nil, e
	}
	if e = json.Unmarshal(b, &inv.Routes); e != nil {
		return inv, nil, e
	}
	inv.Revision = Fingerprint(files)
	return inv, files, nil
}
func (l *Linux) Prepare(c Config, inv Inventory, files []File) (Plan, []File, error) {
	if e := Validate(c, inv); e != nil {
		return Plan{}, nil, e
	}
	plan := Plan{Config: c, Revision: inv.Revision, Changes: []Change{}, Warnings: []string{"应用仅配置 IPv4 地址和路由，不包含 DHCP 服务、DNS 服务、防火墙或 NAT。", "应用后须在 90 秒内确认；超时或服务重启将恢复旧配置。"}}
	after := append([]File{}, files...)
	for _, p := range []Port{c.WAN, c.LAN} {
		var before *Port
		source := -1
		for i, f := range after {
			old, _, _, e := parseStanza(f.Content, p.Interface)
			if e != nil {
				return plan, nil, e
			}
			if old != nil {
				before = old
				source = i
			}
		}
		changed := before == nil || *before != p
		file := filepath.Join(l.Root, "interfaces.d", "pilot-"+p.Interface)
		if changed {
			if source >= 0 {
				f := &after[source]
				_, start, end, _ := parseStanza(f.Content, p.Interface)
				lines := strings.Split(f.Content, "\n")
				replacement := strings.TrimPrefix(Render(p), "auto "+p.Interface+"\n")
				f.Content = strings.Join(lines[:start], "\n") + "\n" + replacement + strings.Join(lines[end:], "\n")
				file = f.Path
			} else {
				f, e := ReadFile(file)
				if e != nil {
					return plan, nil, e
				}
				if f.Exists {
					return plan, nil, fmt.Errorf("目标配置文件已存在，拒绝覆盖")
				}
				after = append(after, File{Path: file, Exists: true, Mode: 0644, Content: ManagedText(p)})
			}
		}
		wasUp := false
		for _, d := range inv.Devices {
			if d.Name == p.Interface {
				wasUp = d.Up
			}
		}
		plan.Changes = append(plan.Changes, Change{WasUp: wasUp, Interface: p.Interface, Before: before, After: p, Changed: changed, File: file})
	}
	plan.Token = Fingerprint(struct {
		Config   Config
		Revision string
	}{c, inv.Revision})
	return plan, after, nil
}
func AtomicFile(path string, b []byte, mode os.FileMode) error {
	f, e := os.CreateTemp(filepath.Dir(path), ".pilot-")
	if e != nil {
		return e
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if e = f.Chmod(mode); e == nil {
		_, e = f.Write(b)
	}
	if e == nil {
		e = f.Sync()
	}
	ce := f.Close()
	if e == nil {
		e = ce
	}
	if e != nil {
		return e
	}
	if e = os.Rename(tmp, path); e != nil {
		return e
	}
	d, e := os.Open(filepath.Dir(path))
	if e != nil {
		return e
	}
	defer d.Close()
	return d.Sync()
}
func (l *Linux) Write(files []File) error {
	for _, f := range files {
		if filepath.Clean(f.Path) != filepath.Join(l.Root, "interfaces") && filepath.Dir(filepath.Clean(f.Path)) != filepath.Join(l.Root, "interfaces.d") {
			return fmt.Errorf("配置路径越界")
		}
		if f.Exists {
			if e := AtomicFile(f.Path, []byte(f.Content), os.FileMode(f.Mode)); e != nil {
				return e
			}
		} else {
			if e := os.Remove(f.Path); e != nil && !os.IsNotExist(e) {
				return e
			}
		}
	}
	return nil
}
func (l *Linux) Activate(changes []Change, rollback bool) error {
	for _, ch := range changes {
		if !ch.Changed {
			continue
		}
		from, to := ch.Before, &ch.After
		if rollback {
			from, to = &ch.After, ch.Before
		}
		f, e := os.CreateTemp("", "pilot-ifupdown-")
		if e != nil {
			return e
		}
		tmp := f.Name()
		f.Close()
		operation := func() error {
			defer os.Remove(tmp)
			if from != nil {
				if e = os.WriteFile(tmp, []byte(Render(*from)), 0600); e != nil {
					return e
				}
				if _, e = run("/usr/sbin/ifdown", "--force", "-i", tmp, "--state-dir", l.stateDir(), ch.Interface); e != nil && !rollback {
					return e
				}
			}
			if to != nil {
				if e = os.WriteFile(tmp, []byte(Render(*to)), 0600); e != nil {
					return e
				}
				if _, e = run("/usr/sbin/ifup", "--force", "-i", tmp, "--state-dir", l.stateDir(), ch.Interface); e != nil {
					return e
				}
			} else {
				if _, e = run("/usr/sbin/ip", "-4", "addr", "flush", "dev", ch.Interface); e != nil {
					return e
				}
				linkState := "down"
				if ch.WasUp {
					linkState = "up"
				}
				_, e = run("/usr/sbin/ip", "link", "set", "dev", ch.Interface, linkState)
				if e != nil {
					return e
				}
			}
			return nil
		}
		if e := operation(); e != nil {
			return e
		}
	}
	return nil
}

func (l *Linux) stateDir() string {
	if l.StateDir != "" {
		return l.StateDir
	}
	return "/run/network"
}
