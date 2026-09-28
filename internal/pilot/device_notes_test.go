package pilot

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Lighten012/Lighten012-Pilot/internal/network"
)

func TestDeviceNotesFollowMACAndPersist(t *testing.T) {
	path := filepath.Join(t.TempDir(), "device-notes.json")
	store, err := loadDeviceNotes(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.set("AA-BB-CC-DD-EE-FF", "  客厅电脑  "); err != nil {
		t.Fatal(err)
	}
	reloaded, err := loadDeviceNotes(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := reloaded.all()["aa:bb:cc:dd:ee:ff"]; got != "客厅电脑" {
		t.Fatalf("note lost: %q", got)
	}
	if err := reloaded.set("aa:bb:cc:dd:ee:ff", ""); err != nil {
		t.Fatal(err)
	}
	if len(reloaded.all()) != 0 {
		t.Fatal("empty note should remove mapping")
	}
}

func TestDeviceNotesAPI(t *testing.T) {
	store, err := loadDeviceNotes(filepath.Join(t.TempDir(), "device-notes.json"))
	if err != nil {
		t.Fatal(err)
	}
	api := (&app{network: &network.Manager{}, notes: store}).routes()
	put := httptest.NewRequest(http.MethodPut, "http://pilot.local/api/lan/device-notes/aa:bb:cc:dd:ee:ff", strings.NewReader(`{"note":"工作电脑"}`))
	put.Header.Set("X-Pilot-Request", "1")
	put.Header.Set("Origin", "http://pilot.local")
	response := httptest.NewRecorder()
	api.ServeHTTP(response, put)
	if response.Code != http.StatusOK {
		t.Fatalf("put: %d %s", response.Code, response.Body.String())
	}
	get := httptest.NewRecorder()
	api.ServeHTTP(get, httptest.NewRequest(http.MethodGet, "/api/lan/device-notes", nil))
	var notes map[string]string
	if err := json.Unmarshal(get.Body.Bytes(), &notes); err != nil || notes["aa:bb:cc:dd:ee:ff"] != "工作电脑" {
		t.Fatalf("get: %s, %v", get.Body.String(), err)
	}
}

func TestDeviceNotesRejectInvalidInput(t *testing.T) {
	store, err := loadDeviceNotes(filepath.Join(t.TempDir(), "device-notes.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range []struct{ mac, note string }{
		{"invalid", "PC"},
		{"aa:bb:cc:dd:ee:ff", strings.Repeat("机", 65)},
		{"aa:bb:cc:dd:ee:ff", "bad\nline"},
	} {
		if err := store.set(item.mac, item.note); err == nil {
			t.Fatalf("accepted %+v", item)
		}
	}
}
