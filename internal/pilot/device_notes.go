package pilot

import (
	"encoding/json"
	"errors"
	"net"
	"os"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"github.com/Lighten012/Lighten012-Pilot/internal/storage"
)

// Device notes are keyed by MAC so a DHCP address change does not lose the label.
type deviceNotes struct {
	mu    sync.RWMutex
	path  string
	notes map[string]string
}

func loadDeviceNotes(path string) (*deviceNotes, error) {
	store := &deviceNotes{path: path, notes: map[string]string{}}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return store, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, &store.notes); err != nil {
		return nil, err
	}
	if store.notes == nil {
		store.notes = map[string]string{}
	}
	for mac, note := range store.notes {
		if _, _, err := validDeviceNote(mac, note); err != nil {
			return nil, err
		}
	}
	return store, nil
}

func validDeviceNote(mac, note string) (string, string, error) {
	parsed, err := net.ParseMAC(mac)
	if err != nil || len(parsed) != 6 {
		return "", "", errors.New("MAC 地址无效")
	}
	mac = parsed.String()
	note = strings.TrimSpace(note)
	if utf8.RuneCountInString(note) > 64 {
		return "", "", errors.New("备注最多 64 个字符")
	}
	for _, char := range note {
		if unicode.IsControl(char) {
			return "", "", errors.New("备注不能包含控制字符")
		}
	}
	return mac, note, nil
}

func (store *deviceNotes) all() map[string]string {
	store.mu.RLock()
	defer store.mu.RUnlock()
	result := make(map[string]string, len(store.notes))
	for mac, note := range store.notes {
		result[mac] = note
	}
	return result
}

func (store *deviceNotes) set(mac, note string) error {
	mac, note, err := validDeviceNote(mac, note)
	if err != nil {
		return err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	next := make(map[string]string, len(store.notes))
	for key, value := range store.notes {
		next[key] = value
	}
	if note == "" {
		delete(next, mac)
	} else {
		next[mac] = note
	}
	data, err := json.MarshalIndent(next, "", "  ")
	if err != nil {
		return err
	}
	if err := storage.WriteAtomic(store.path, append(data, '\n'), 0600); err != nil {
		return err
	}
	store.notes = next
	return nil
}
