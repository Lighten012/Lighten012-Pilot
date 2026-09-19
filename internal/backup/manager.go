package backup

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/Lighten012/Lighten012-Pilot/internal/firewall"
	"github.com/Lighten012/Lighten012-Pilot/internal/network"
	"github.com/Lighten012/Lighten012-Pilot/internal/services"
)

// Only declarative Pilot settings are portable. Never import paths, commands,
// journal transactions, DHCP leases, or host firewall snapshots.
type Bundle struct {
	Format    string          `json:"format"`
	Version   int             `json:"version"`
	CreatedAt int64           `json:"createdAt"`
	Network   network.Config  `json:"network"`
	Services  services.Config `json:"services"`
	Firewall  firewall.Config `json:"firewall"`
}
type Plan struct {
	Backup  Bundle   `json:"backup"`
	Token   string   `json:"token"`
	Changes []string `json:"changes"`
}
type Transaction struct {
	ID       string `json:"id"`
	Phase    string `json:"phase"`
	Deadline int64  `json:"deadline"`
	Before   Bundle `json:"before"`
	Target   Bundle `json:"target"`
}
type State struct {
	Pending    *Transaction `json:"pending"`
	Event      string       `json:"event"`
	ServerTime int64        `json:"serverTime"`
}
type Engine interface {
	Snapshot() (Bundle, error)
	Validate(Bundle) (Bundle, error)
	Apply(Bundle) error
}
type Manager struct {
	mu      sync.Mutex
	gate    *sync.Mutex
	engine  Engine
	file    string
	state   State
	working bool
	retry   time.Time
	timeout time.Duration
}

func New(e Engine, gate *sync.Mutex, dir string, timeout time.Duration) (*Manager, error) {
	m := &Manager{engine: e, gate: gate, file: filepath.Join(dir, "backup.json"), timeout: timeout}
	b, err := os.ReadFile(m.file)
	if err == nil {
		if err = json.Unmarshal(b, &m.state); err != nil {
			return nil, err
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	// Interrupted restores are never silently confirmed, even before the deadline.
	if m.state.Pending != nil {
		m.state.Pending.Phase = "rolling_back"
		m.start(true)
	}
	return m, nil
}
func (m *Manager) save(s State) error {
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	if err = network.AtomicFile(m.file, b, 0600); err != nil {
		return err
	}
	m.state = s
	return nil
}
func (m *Manager) Busy() bool { m.mu.Lock(); defer m.mu.Unlock(); return m.state.Pending != nil }
func (m *Manager) State() State {
	m.mu.Lock()
	defer m.mu.Unlock()
	// Return a deep copy, including rule and record slices.
	b, _ := json.Marshal(m.state)
	var s State
	_ = json.Unmarshal(b, &s)
	s.ServerTime = time.Now().UnixMilli()
	return s
}
func (m *Manager) Export() (Bundle, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.state.Pending != nil {
		return Bundle{}, fmt.Errorf("恢复尚未结束，请先确认或回滚")
	}
	return m.engine.Snapshot()
}
func same(a, b any) bool { return network.Fingerprint(a) == network.Fingerprint(b) }
func (m *Manager) preview(b Bundle) (Plan, error) {
	if m.state.Pending != nil {
		return Plan{}, fmt.Errorf("已有恢复正在进行")
	}
	current, err := m.engine.Snapshot()
	if err != nil {
		return Plan{}, err
	}
	b, err = m.engine.Validate(b)
	if err != nil {
		return Plan{}, err
	}
	current.CreatedAt = 0
	changes := []string{}
	if !same(current.Network, b.Network) {
		changes = append(changes, "WAN / LAN 地址配置")
	}
	if !same(current.Services, b.Services) {
		changes = append(changes, "DHCP / DNS 配置及解析记录")
	}
	if !same(current.Firewall, b.Firewall) {
		changes = append(changes, "防火墙 / NAT / 端口映射")
	}
	return Plan{Backup: b, Changes: changes, Token: network.Fingerprint(struct{ Before, After Bundle }{current, b})}, nil
}
func (m *Manager) Preview(b Bundle) (Plan, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.preview(b)
}
func (m *Manager) Apply(req Plan) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, err := m.preview(req.Backup)
	if err != nil {
		return "", err
	}
	if req.Token != p.Token {
		return "", fmt.Errorf("配置已变化，请重新预览备份")
	}
	if len(p.Changes) == 0 {
		return "", fmt.Errorf("备份与当前配置一致，无需恢复")
	}
	before, err := m.engine.Snapshot()
	if err != nil {
		return "", err
	}
	id := make([]byte, 16)
	if _, err = rand.Read(id); err != nil {
		return "", err
	}
	tx := &Transaction{ID: hex.EncodeToString(id), Phase: "applying", Before: before, Target: p.Backup}
	if err = m.save(State{Pending: tx, Event: "正在恢复配置，请等待"}); err != nil {
		return "", err
	}
	m.start(false)
	return tx.ID, nil
}

// Caller holds mu, or is constructing an unpublished manager. The shared gate
// serializes this worker against all ordinary mutations and reconciliation.
func (m *Manager) start(rollback bool) {
	m.working = true
	go func() {
		m.gate.Lock()
		defer m.gate.Unlock()
		m.mu.Lock()
		tx := *m.state.Pending
		m.mu.Unlock()
		var err error
		if !rollback {
			err = m.engine.Apply(tx.Target)
		}
		if !rollback && err == nil {
			m.mu.Lock()
			tx.Phase = "awaiting_confirmation"
			tx.Deadline = time.Now().Add(m.timeout).UnixMilli()
			err = m.save(State{Pending: &tx, Event: "配置已恢复，请在倒计时结束前确认网络正常"})
			m.mu.Unlock()
		}
		if rollback || err != nil {
			reason := "已恢复到导入前的配置"
			if err != nil {
				reason = "恢复失败，已还原原配置: " + err.Error()
			}
			m.mu.Lock()
			tx.Phase = "rolling_back"
			_ = m.save(State{Pending: &tx, Event: "正在还原原配置"})
			m.mu.Unlock()
			re := m.engine.Apply(tx.Before)
			m.mu.Lock()
			if re == nil {
				re = m.save(State{Event: reason})
			}
			if re != nil {
				tx.Phase = "rollback_failed"
				m.state = State{Pending: &tx, Event: "原配置恢复未完成，将重试: " + re.Error()}
				_ = m.save(m.state)
				m.retry = time.Now().Add(10 * time.Second)
			}
			m.mu.Unlock()
		}
		m.mu.Lock()
		m.working = false
		log.Printf("配置备份恢复: %s", m.state.Event)
		m.mu.Unlock()
	}()
}
func (m *Manager) Confirm(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	p := m.state.Pending
	if p == nil || p.ID != id || m.working || p.Phase != "awaiting_confirmation" {
		return fmt.Errorf("恢复尚未完成或事务已结束")
	}
	if time.Now().UnixMilli() >= p.Deadline {
		return fmt.Errorf("确认已超时，正在回滚")
	}
	return m.save(State{Event: "恢复已确认，配置已保存"})
}
func (m *Manager) Rollback(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	p := m.state.Pending
	if p == nil || p.ID != id || m.working {
		return fmt.Errorf("事务不存在或正在执行，请稍后重试")
	}
	tx := *p
	tx.Phase = "rolling_back"
	if err := m.save(State{Pending: &tx, Event: "正在还原原配置"}); err != nil {
		return err
	}
	m.start(true)
	return nil
}
func (m *Manager) Tick() {
	m.mu.Lock()
	defer m.mu.Unlock()
	p := m.state.Pending
	if p != nil && !m.working && !time.Now().Before(m.retry) && (p.Phase != "awaiting_confirmation" || time.Now().UnixMilli() >= p.Deadline) {
		m.start(true)
	}
}
