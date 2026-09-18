package firewall

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/Lighten012/Lighten012-Pilot/internal/network"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type Plan struct {
	Config   Config `json:"config"`
	Revision string `json:"revision"`
	Token    string `json:"token"`
	Text     string `json:"text"`
}
type Pending struct {
	ID       string `json:"id"`
	Deadline int64  `json:"deadline"`
	Config   Config `json:"config"`
}
type Journal struct {
	Saved    Config   `json:"saved"`
	Pending  *Pending `json:"pending"`
	Baseline string   `json:"baseline"`
}
type State struct {
	Config     Config   `json:"config"`
	Pending    *Pending `json:"pending"`
	Available  bool     `json:"available"`
	Forwarding bool     `json:"forwarding"`
	Ready      bool     `json:"ready"`
	Event      string   `json:"event"`
	ServerTime int64    `json:"serverTime"`
}
type Manager struct {
	mu           sync.Mutex
	backend      Backend
	file         string
	journal      Journal
	networkState func() (network.Status, error)
	timeout      time.Duration
	ready        bool
	event        string
	retry        time.Time
}

func NewManager(b Backend, dir string, ns func() (network.Status, error), timeout time.Duration) (*Manager, error) {
	if e := os.MkdirAll(dir, 0700); e != nil {
		return nil, e
	}
	m := &Manager{backend: b, file: filepath.Join(dir, "firewall.json"), networkState: ns, timeout: timeout, journal: Journal{Saved: Default()}, event: "尚未启用转发防火墙"}
	data, e := os.ReadFile(m.file)
	if e == nil {
		if e = json.Unmarshal(data, &m.journal); e != nil {
			return nil, e
		}
	} else if !os.IsNotExist(e) {
		return nil, e
	}
	if m.journal.Baseline != "" {
		m.restore("已恢复保存的防火墙配置；未确认变更已回滚")
	}
	return m, nil
}
func (m *Manager) persist(j Journal) error {
	b, e := json.MarshalIndent(j, "", "  ")
	if e != nil {
		return e
	}
	if e = network.AtomicFile(m.file, b, 0600); e != nil {
		return e
	}
	m.journal = j
	return nil
}
func (m *Manager) restore(event string) error {
	m.ready = false
	if e := m.backend.Apply(m.journal.Saved, m.journal.Baseline); e != nil {
		m.event = "恢复失败，将重试: " + e.Error()
		m.retry = time.Now().Add(10 * time.Second)
		return e
	}
	j := m.journal
	j.Pending = nil
	if e := m.persist(j); e != nil {
		m.event = "恢复状态保存失败: " + e.Error()
		m.retry = time.Now().Add(10 * time.Second)
		return e
	}
	m.ready = true
	m.event = event
	log.Printf("防火墙: %s", event)
	return nil
}
func (m *Manager) Enabled() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.journal.Saved.Enabled || m.journal.Pending != nil || m.journal.Baseline != "" && !m.ready
}
func (m *Manager) preview(c Config) (Plan, error) {
	if m.journal.Pending != nil {
		return Plan{}, fmt.Errorf("请先确认或回滚当前变更")
	}
	if m.journal.Baseline != "" && !m.ready {
		return Plan{}, fmt.Errorf("旧配置恢复尚未完成")
	}
	if e := m.backend.Available(); e != nil {
		return Plan{}, e
	}
	c, e := Normalize(c)
	if e != nil {
		return Plan{}, e
	}
	s, e := m.networkState()
	if e != nil {
		return Plan{}, e
	}
	c, e = Bind(c, s)
	if e != nil {
		return Plan{}, e
	}
	p := Plan{Config: c, Revision: network.Fingerprint(m.journal), Text: Render(c)}
	p.Token = network.Fingerprint(struct {
		Config   Config
		Revision string
	}{c, p.Revision})
	if e = m.backend.Check(c); e != nil {
		return p, e
	}
	return p, nil
}
func (m *Manager) Preview(c Config) (Plan, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.preview(c)
}
func (m *Manager) Apply(req Plan) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, e := m.preview(req.Config)
	if e != nil {
		return "", e
	}
	if req.Token != p.Token || req.Revision != p.Revision {
		return "", fmt.Errorf("配置已变化，请重新预览")
	}
	j := m.journal
	if j.Baseline == "" {
		j.Baseline, e = m.backend.Forwarding()
		if e != nil {
			return "", e
		}
		if j.Baseline != "0" && j.Baseline != "1" {
			return "", fmt.Errorf("无法读取 IPv4 转发状态")
		}
	}
	id := make([]byte, 16)
	if _, e = rand.Read(id); e != nil {
		return "", e
	}
	j.Pending = &Pending{ID: hex.EncodeToString(id), Deadline: time.Now().Add(m.timeout).UnixMilli(), Config: p.Config}
	if e = m.persist(j); e != nil {
		return "", e
	}
	if e = m.backend.Apply(p.Config, j.Baseline); e != nil {
		re := m.restore("应用失败，已恢复原配置")
		if re != nil {
			return "", fmt.Errorf("应用失败: %v；恢复失败: %w", e, re)
		}
		return "", e
	}
	m.ready = true
	m.event = "已临时应用，请确认网络正常；超时自动回滚"
	log.Printf("防火墙临时应用 enabled=%t nat=%t rules=%d forwards=%d", p.Config.Enabled, p.Config.NAT, len(p.Config.Rules), len(p.Config.Forwards))
	return j.Pending.ID, nil
}
func (m *Manager) Confirm(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	p := m.journal.Pending
	if p == nil || p.ID != id || !m.ready {
		return fmt.Errorf("变更不存在或不可确认")
	}
	if time.Now().UnixMilli() >= p.Deadline {
		return fmt.Errorf("已超时，等待自动回滚")
	}
	j := m.journal
	j.Saved = p.Config
	j.Pending = nil
	if e := m.persist(j); e != nil {
		return e
	}
	m.event = "防火墙配置已确认并保存"
	log.Print(m.event)
	return nil
}
func (m *Manager) Rollback(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.journal.Pending == nil || m.journal.Pending.ID != id {
		return fmt.Errorf("变更不存在")
	}
	return m.restore("已恢复变更前的防火墙配置")
}
func (m *Manager) Tick() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if time.Now().Before(m.retry) {
		return
	}
	if m.journal.Baseline != "" && (!m.ready || m.journal.Pending != nil && time.Now().UnixMilli() >= m.journal.Pending.Deadline) {
		_ = m.restore("未确认或异常变更已自动恢复")
	}
}
func (m *Manager) State() (State, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c := m.journal.Saved
	if m.journal.Pending != nil {
		c = m.journal.Pending.Config
	}
	c.Rules = append([]Rule{}, c.Rules...)
	c.Forwards = append([]Forward{}, c.Forwards...)
	s := State{Config: c, Available: m.backend.Available() == nil, Ready: m.ready, Event: m.event, ServerTime: time.Now().UnixMilli()}
	if m.journal.Pending != nil {
		p := *m.journal.Pending
		p.Config = c
		s.Pending = &p
	}
	f, e := m.backend.Forwarding()
	if e == nil {
		s.Forwarding = f == "1"
	}
	if !s.Available {
		s.Event = "iptables 未安装，暂不能应用规则"
	}
	return s, nil
}
