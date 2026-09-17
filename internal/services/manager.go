package services

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Lighten012/Lighten012-Pilot/internal/network"
)

type Lease struct {
	Address  string `json:"address"`
	MAC      string `json:"mac"`
	Hostname string `json:"hostname"`
	Expires  int64  `json:"expires"`
}
type State struct {
	Config      Config        `json:"config"`
	Revision    string        `json:"revision"`
	LAN         *network.Port `json:"lan"`
	DNSRunning  bool          `json:"dnsRunning"`
	DHCPRunning bool          `json:"dhcpRunning"`
	Stats       Counters      `json:"stats"`
	Leases      []Lease       `json:"leases"`
	Event       string        `json:"event"`
}
type Plan struct {
	Config   Config `json:"config"`
	Revision string `json:"revision"`
	Token    string `json:"token"`
	DHCPText string `json:"dhcpText"`
}
type ApplyRequest struct {
	Config   Config `json:"config"`
	Revision string `json:"revision"`
	Token    string `json:"token"`
}
type Manager struct {
	mu           sync.Mutex
	dir          string
	config       Config
	runtime      *Runtime
	networkState func() (network.Status, error)
	event        string
	nextRetry    time.Time
}

func NewManager(dir string, networkState func() (network.Status, error)) (*Manager, error) {
	if e := os.MkdirAll(dir, 0700); e != nil {
		return nil, e
	}
	m := &Manager{dir: dir, config: Default(), networkState: networkState, event: "尚未启用 DHCP / DNS"}
	b, e := os.ReadFile(filepath.Join(dir, "services.json"))
	if e == nil {
		if e = json.Unmarshal(b, &m.config); e != nil {
			return nil, e
		}
		m.config, e = Normalize(m.config)
		if e != nil {
			return nil, e
		}
	} else if !os.IsNotExist(e) {
		return nil, e
	}
	m.Reconcile()
	return m, nil
}
func (m *Manager) Enabled() bool { m.mu.Lock(); defer m.mu.Unlock(); return m.config.Enabled() }
func (m *Manager) preview(c Config) (Plan, error) {
	// Normalize a private copy so polling and failed requests cannot mutate state.
	c.DNS.Records = append([]Record(nil), c.DNS.Records...)
	c, e := Normalize(c)
	if e != nil {
		return Plan{}, e
	}
	s, e := m.networkState()
	if e != nil {
		return Plan{}, e
	}
	if e = ValidateLAN(c, s); e != nil {
		return Plan{}, e
	}
	revision := network.Fingerprint(struct {
		Config  Config
		Network string
		Saved   *network.Config
		Pending *network.Pending
	}{m.config, s.Inventory.Revision, s.Saved, s.Pending})
	p := Plan{Config: c, Revision: revision}
	p.Token = network.Fingerprint(struct {
		Config   Config
		Revision string
	}{c, revision})
	if c.DHCP.Enabled {
		p.DHCPText = DHCPText(c, m.dir)
	}
	return p, nil
}
func (m *Manager) Preview(c Config) (Plan, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.preview(c)
}
func (m *Manager) Apply(req ApplyRequest) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, e := m.preview(req.Config)
	if e != nil {
		return e
	}
	if req.Revision != p.Revision || req.Token != p.Token {
		return fmt.Errorf("配置或 LAN 状态已变化，请重新预览")
	}
	old := m.config
	m.runtime.Stop()
	m.runtime = nil
	r, e := Start(p.Config, m.dir)
	if e == nil {
		b, _ := json.MarshalIndent(p.Config, "", "  ")
		e = network.AtomicFile(filepath.Join(m.dir, "services.json"), b, 0600)
	}
	if e != nil {
		r.Stop()
		restored, re := Start(old, m.dir)
		m.runtime = restored
		m.event = "应用失败，已恢复之前的服务配置"
		if re != nil {
			m.event = "应用失败，旧服务恢复也未完成: " + re.Error()
		}
		return fmt.Errorf("%s: %w", m.event, e)
	}
	m.config = p.Config
	m.runtime = r
	m.event = "配置已保存并应用"
	return nil
}
func (m *Manager) Reconcile() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.config.Enabled() {
		return
	}
	s, e := m.networkState()
	if e == nil {
		e = ValidateLAN(m.config, s)
	}
	if e != nil {
		m.runtime.Stop()
		m.runtime = nil
		m.event = "服务已暂停: " + e.Error()
		return
	}
	if m.runtime != nil && m.runtime.Error() == "" {
		return
	}
	if time.Now().Before(m.nextRetry) {
		return
	}
	m.nextRetry = time.Now().Add(10 * time.Second)
	m.runtime.Stop()
	r, e := Start(m.config, m.dir)
	m.runtime = r
	if e != nil {
		m.event = "服务启动失败，将重试: " + e.Error()
	} else {
		m.event = "已恢复保存的服务配置"
	}
}
func (m *Manager) State() (State, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, e := m.networkState()
	if e != nil {
		return State{}, e
	}
	c := m.config
	c.DNS.Records = append([]Record{}, c.DNS.Records...)
	state := State{Config: c, Event: m.event, Leases: []Lease{}}
	if s.Saved != nil {
		lan := s.Saved.LAN
		state.LAN = &lan
	}
	state.Revision = network.Fingerprint(m.config)
	if m.runtime != nil {
		state.DNSRunning = m.config.DNS.Enabled && m.runtime.Error() == ""
		state.DHCPRunning = m.config.DHCP.Enabled && m.runtime.alive.Load()
		if m.runtime.resolver != nil {
			state.Stats = m.runtime.resolver.Stats()
		}
		if err := m.runtime.Error(); err != "" {
			state.Event = err
		}
	}
	if b, e := os.ReadFile(filepath.Join(m.dir, "dhcp.leases")); e == nil {
		state.Leases = ParseLeases(string(b), time.Now().Unix())
	}
	return state, nil
}
func ParseLeases(data string, now int64) []Lease {
	leases := []Lease{}
	for _, line := range strings.Split(data, "\n") {
		f := strings.Fields(line)
		if len(f) < 4 {
			continue
		}
		expires, e := strconv.ParseInt(f[0], 10, 64)
		if e != nil || expires != 0 && expires <= now {
			continue
		}
		leases = append(leases, Lease{Address: f[2], MAC: f[1], Hostname: f[3], Expires: expires * 1000})
	}
	return leases
}
