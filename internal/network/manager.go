package network

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type Transaction struct {
	ID       string `json:"id"`
	Phase    string `json:"phase"`
	Deadline int64  `json:"deadline"`
	Plan     Plan   `json:"plan"`
	Before   []File `json:"before"`
	After    []File `json:"after"`
}
type Journal struct {
	Saved   *Config      `json:"saved"`
	Draft   *Config      `json:"draft"`
	Pending *Transaction `json:"pending"`
	Event   string       `json:"event"`
}
type Status struct {
	Inventory  Inventory `json:"inventory"`
	Saved      *Config   `json:"saved"`
	Draft      *Config   `json:"draft"`
	Pending    *Pending  `json:"pending"`
	Event      string    `json:"event"`
	ServerTime int64     `json:"serverTime"`
}
type Pending struct {
	ID       string `json:"id"`
	Phase    string `json:"phase"`
	Deadline int64  `json:"deadline"`
	Config   Config `json:"config"`
}
type ApplyRequest struct {
	Config   Config `json:"config"`
	Revision string `json:"revision"`
	Token    string `json:"token"`
}
type Manager struct {
	mu      sync.Mutex
	backend Backend
	file    string
	journal Journal
	timeout time.Duration
	working bool
}

func NewManager(backend Backend, dir string, timeout time.Duration) (*Manager, error) {
	if e := os.MkdirAll(dir, 0700); e != nil {
		return nil, e
	}
	m := &Manager{backend: backend, file: filepath.Join(dir, "network.json"), timeout: timeout}
	b, e := os.ReadFile(m.file)
	if e == nil {
		if e = json.Unmarshal(b, &m.journal); e != nil {
			return nil, fmt.Errorf("无法解析持久化回滚记录: %w", e)
		}
	} else if !os.IsNotExist(e) {
		return nil, e
	}
	return m, nil
}
func (m *Manager) persist(j Journal) error {
	b, e := json.MarshalIndent(j, "", "  ")
	if e != nil {
		return e
	}
	if e = AtomicFile(m.file, b, 0600); e != nil {
		return e
	}
	m.journal = j
	return nil
}
func (m *Manager) Status() (Status, error) {
	inv, _, e := m.backend.Inspect()
	if e != nil {
		return Status{}, e
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	s := Status{Inventory: inv, Saved: m.journal.Saved, Draft: m.journal.Draft, Event: m.journal.Event, ServerTime: time.Now().UnixMilli()}
	if p := m.journal.Pending; p != nil {
		s.Pending = &Pending{ID: p.ID, Phase: p.Phase, Deadline: p.Deadline, Config: p.Plan.Config}
	}
	return s, nil
}
func (m *Manager) preview(c Config) (Plan, []File, []File, error) {
	c = Canonical(c)
	if m.journal.Saved != nil && (m.journal.Saved.WAN.Interface != c.WAN.Interface || m.journal.Saved.LAN.Interface != c.LAN.Interface) {
		return Plan{}, nil, nil, fmt.Errorf("当前版本不支持迁移已确认的接口角色，可修改原接口的地址设置")
	}
	inv, files, e := m.backend.Inspect()
	if e != nil {
		return Plan{}, nil, nil, e
	}
	p, after, e := m.backend.Prepare(c, inv, files)
	if e != nil {
		return p, nil, nil, e
	}
	// Back up only files this operation changes. Include tombstones for newly
	// created snippets so rollback removes them without touching unrelated files.
	before := []File{}
	writes := []File{}
	old := map[string]File{}
	for _, f := range files {
		old[f.Path] = f
	}
	for _, f := range after {
		o, exists := old[f.Path]
		if exists && o == f {
			continue
		}
		if !exists {
			o = File{Path: f.Path, Mode: f.Mode}
		}
		before = append(before, o)
		writes = append(writes, f)
	}
	return p, before, writes, nil
}
func (m *Manager) Preview(c Config) (Plan, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.journal.Pending != nil {
		return Plan{}, fmt.Errorf("有待确认或待恢复的配置，请先处理")
	}
	p, _, _, e := m.preview(c)
	return p, e
}
func (m *Manager) Save(c Config) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.journal.Pending != nil {
		return fmt.Errorf("变更进行中，不能覆盖草稿")
	}
	p, _, _, e := m.preview(c)
	if e != nil {
		return e
	}
	j := m.journal
	j.Draft = &p.Config
	j.Event = "草稿已保存，尚未应用到系统"
	return m.persist(j)
}
func (m *Manager) Apply(req ApplyRequest) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.journal.Pending != nil {
		return "", fmt.Errorf("已有变更正在进行")
	}
	p, before, after, e := m.preview(req.Config)
	if e != nil {
		return "", e
	}
	if req.Revision != p.Revision || req.Token != p.Token {
		return "", fmt.Errorf("配置或系统状态已变化，请重新预览")
	}
	b := make([]byte, 16)
	if _, e = rand.Read(b); e != nil {
		return "", e
	}
	id := hex.EncodeToString(b)
	tx := &Transaction{ID: id, Phase: "applying", Deadline: time.Now().Add(m.timeout).UnixMilli(), Plan: p, Before: before, After: after}
	j := m.journal
	j.Pending = tx
	j.Event = "正在应用配置"
	if e = m.persist(j); e != nil {
		return "", e
	}
	m.working = true
	go m.activate(tx)
	return id, nil
}
func (m *Manager) activate(tx *Transaction) {
	e := m.backend.Write(tx.After)
	if e == nil {
		e = m.backend.Activate(tx.Plan.Changes, false)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	defer func() { m.working = false }()
	if e != nil {
		m.rollbackLocked("应用失败，已尝试恢复: " + e.Error())
		return
	}
	j := m.journal
	copy := *j.Pending
	copy.Phase = "awaiting_confirmation"
	copy.Deadline = time.Now().Add(m.timeout).UnixMilli()
	j.Pending = &copy
	j.Event = "配置已临时应用，等待确认"
	if e = m.persist(j); e != nil {
		m.rollbackLocked("无法保存待确认状态，恢复旧配置")
	}
}
func (m *Manager) Confirm(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	p := m.journal.Pending
	if p == nil || p.ID != id {
		return fmt.Errorf("事务不存在或已结束")
	}
	if p.Phase != "awaiting_confirmation" {
		return fmt.Errorf("当前阶段不能确认")
	}
	if time.Now().UnixMilli() >= p.Deadline {
		return fmt.Errorf("确认已超时，正在自动回滚")
	}
	j := m.journal
	c := p.Plan.Config
	j.Saved = &c
	j.Draft = &c
	j.Pending = nil
	j.Event = "配置已确认并持久化"
	return m.persist(j)
}
func (m *Manager) Rollback(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	p := m.journal.Pending
	if p == nil || p.ID != id {
		return fmt.Errorf("事务不存在或已结束")
	}
	if m.working {
		return fmt.Errorf("配置正在应用，请稍后恢复")
	}
	return m.rollbackLocked("已恢复应用前的配置")
}
func (m *Manager) rollbackLocked(event string) error {
	p := m.journal.Pending
	if p == nil {
		return nil
	}
	j := m.journal
	t := *p
	t.Phase = "rolling_back"
	j.Pending = &t
	j.Event = "正在恢复旧配置"
	if e := m.persist(j); e != nil {
		return e
	}
	e := m.backend.Write(p.Before)
	if e == nil {
		e = m.backend.Activate(p.Plan.Changes, true)
	}
	j = m.journal
	if e != nil {
		t.Phase = "rollback_failed"
		j.Pending = &t
		j.Event = "自动恢复未完成，将重试。" + e.Error()
		_ = m.persist(j)
		return e
	}
	j.Pending = nil
	j.Event = event
	return m.persist(j)
}

// Recovery runs before accepting requests. Any unconfirmed transaction is
// rolled back even if its deadline has not elapsed (daemon or host restart).
func (m *Manager) Recover() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.rollbackLocked("服务重启后已恢复未确认的网络配置")
}
func (m *Manager) Tick() {
	m.mu.Lock()
	defer m.mu.Unlock()
	p := m.journal.Pending
	if p != nil && !m.working && (p.Phase != "awaiting_confirmation" || time.Now().UnixMilli() >= p.Deadline) {
		_ = m.rollbackLocked("未确认或异常的变更已自动恢复")
	}
}
