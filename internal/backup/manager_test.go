package backup

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

type fakeEngine struct {
	mu      sync.Mutex
	current Bundle
	fail    bool
	failOld bool
}

func (f *fakeEngine) Snapshot() (Bundle, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.current, nil
}
func (f *fakeEngine) Validate(b Bundle) (Bundle, error) {
	if b.Version != 1 {
		return b, fmt.Errorf("version")
	}
	return b, nil
}
func (f *fakeEngine) Apply(b Bundle) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.current = b
	if f.fail && b.Network.LAN.Address == "new" || f.failOld && b.Network.LAN.Address == "old" {
		return fmt.Errorf("injected failure")
	}
	return nil
}

func TestFailedRollbackRetainsJournalAndRetries(t *testing.T) {
	m, f, b := fixture(t)
	f.fail = true
	f.failOld = true
	begin(t, m, b)
	await(t, m, "rollback_failed")
	if !m.Busy() {
		t.Fatal("failed rollback released mutation interlock")
	}
	data, e := os.ReadFile(m.file)
	if e != nil {
		t.Fatal(e)
	}
	var saved State
	if e = json.Unmarshal(data, &saved); e != nil {
		t.Fatal(e)
	}
	if saved.Pending == nil || saved.Pending.Before.Network.LAN.Address != "old" {
		t.Fatal("lost recovery snapshot")
	}
	f.mu.Lock()
	f.failOld = false
	f.mu.Unlock()
	m.mu.Lock()
	m.retry = time.Time{}
	m.mu.Unlock()
	m.Tick()
	await(t, m, "")
	c, _ := f.Snapshot()
	if c.Network.LAN.Address != "old" {
		t.Fatal(c)
	}
}
func fixture(t *testing.T) (*Manager, *fakeEngine, Bundle) {
	t.Helper()
	b := Bundle{Version: 1}
	b.Network.LAN.Address = "old"
	f := &fakeEngine{current: b}
	m, e := New(f, &sync.Mutex{}, t.TempDir(), 10*time.Second)
	if e != nil {
		t.Fatal(e)
	}
	b.Network.LAN.Address = "new"
	return m, f, b
}
func await(t *testing.T, m *Manager, phase string) {
	t.Helper()
	end := time.Now().Add(3 * time.Second)
	for time.Now().Before(end) {
		m.mu.Lock()
		working := m.working
		m.mu.Unlock()
		s := m.State()
		if !working && ((phase == "" && s.Pending == nil) || (s.Pending != nil && s.Pending.Phase == phase)) {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("state %+v", m.State())
}
func begin(t *testing.T, m *Manager, b Bundle) string {
	t.Helper()
	p, e := m.Preview(b)
	if e != nil {
		t.Fatal(e)
	}
	id, e := m.Apply(p)
	if e != nil {
		t.Fatal(e)
	}
	return id
}
func TestRestoreConfirmAndStalePreview(t *testing.T) {
	m, f, b := fixture(t)
	p, _ := m.Preview(b)
	f.current.Network.LAN.Address = "changed"
	if _, e := m.Apply(p); e == nil {
		t.Fatal("accepted stale plan")
	}
	id := begin(t, m, b)
	await(t, m, "awaiting_confirmation")
	if _, e := m.Export(); e == nil {
		t.Fatal("export pending")
	}
	if e := m.Confirm("wrong"); e == nil {
		t.Fatal("wrong id")
	}
	if e := m.Confirm(id); e != nil {
		t.Fatal(e)
	}
	if m.Busy() {
		t.Fatal("still busy")
	}
	c, _ := f.Snapshot()
	if c.Network.LAN.Address != "new" {
		t.Fatal(c)
	}
}
func TestRestoreFailureRollsBackPartialChanges(t *testing.T) {
	m, f, b := fixture(t)
	f.fail = true
	begin(t, m, b)
	await(t, m, "")
	c, _ := f.Snapshot()
	if c.Network.LAN.Address != "old" {
		t.Fatal("partial change remained")
	}
}
func TestTimeoutAndManualRollback(t *testing.T) {
	for _, timeout := range []bool{false, true} {
		m, f, b := fixture(t)
		if timeout {
			m.timeout = 100 * time.Millisecond
		}
		id := begin(t, m, b)
		await(t, m, "awaiting_confirmation")
		if timeout {
			time.Sleep(120 * time.Millisecond)
			if e := m.Confirm(id); e == nil {
				t.Fatal("late confirm")
			}
			m.Tick()
		} else {
			if e := m.Rollback(id); e != nil {
				t.Fatal(e)
			}
		}
		await(t, m, "")
		c, _ := f.Snapshot()
		if c.Network.LAN.Address != "old" {
			t.Fatal(c)
		}
	}
}
func TestRestartRollsBackAllPhases(t *testing.T) {
	for _, phase := range []string{"applying", "awaiting_confirmation", "rolling_back", "rollback_failed"} {
		dir := t.TempDir()
		old := Bundle{Version: 1}
		old.Network.LAN.Address = "old"
		target := old
		target.Network.LAN.Address = "new"
		data, _ := json.Marshal(State{Pending: &Transaction{ID: "id", Phase: phase, Before: old, Target: target, Deadline: time.Now().Add(time.Hour).UnixMilli()}})
		if e := os.WriteFile(filepath.Join(dir, "backup.json"), data, 0600); e != nil {
			t.Fatal(e)
		}
		f := &fakeEngine{current: target}
		m, e := New(f, &sync.Mutex{}, dir, time.Second)
		if e != nil {
			t.Fatal(e)
		}
		await(t, m, "")
		c, _ := f.Snapshot()
		if c.Network.LAN.Address != "old" {
			t.Fatal(phase)
		}
	}
}
