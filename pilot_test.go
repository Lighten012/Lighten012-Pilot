package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"path/filepath"
	"strings"
	"testing"

	"github.com/miekg/dns"
)

func TestLocalRecordsAndPersistence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	c, err := normalize(Config{Upstream: "119.29.29.29", Records: []Record{{Name: "Lighten012.Home.", Type: "a", Value: "192.168.60.1"}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := saveConfig(path, c); err != nil {
		t.Fatal(err)
	}
	loaded, err := loadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Records[0].Name != "lighten012.home" || loaded.Records[0].TTL != 60 {
		t.Fatalf("unexpected loaded config: %+v", loaded)
	}
	r := newResolver(loaded, netip.MustParsePrefix("127.0.0.0/8"))
	query := new(dns.Msg)
	query.SetQuestion("LIGHTEN012.HOME.", dns.TypeA)
	answer := r.resolve(context.Background(), query, "udp")
	if len(answer.Answer) != 1 || answer.Answer[0].(*dns.A).A.String() != "192.168.60.1" {
		t.Fatalf("unexpected A response: %+v", answer)
	}
	query.SetQuestion("lighten012.home.", dns.TypeAAAA)
	answer = r.resolve(context.Background(), query, "udp")
	if answer.Rcode != dns.RcodeSuccess || len(answer.Answer) != 0 || !answer.Authoritative {
		t.Fatalf("expected local NODATA: %+v", answer)
	}
	if stats := r.stats(); stats.Local != 2 || stats.Forwarded != 0 {
		t.Fatalf("unexpected stats: %+v", stats)
	}
}

func TestConfigAPIUpdatesLiveResolver(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	r := newResolver(defaultConfig(), netip.MustParsePrefix("127.0.0.0/8"))
	api := (&app{resolver: r, path: path}).routes()
	request := httptest.NewRequest(http.MethodPut, "http://pilot.local/api/config", strings.NewReader(`{"upstream":"119.29.29.29","records":[{"name":"pilot.home","type":"A","value":"192.168.60.1","ttl":120}]}`))
	request.Header.Set("X-Pilot-Request", "1")
	request.Header.Set("Origin", "http://pilot.local")
	response := httptest.NewRecorder()
	api.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("save failed: %d %s", response.Code, response.Body.String())
	}
	query := new(dns.Msg)
	query.SetQuestion("pilot.home.", dns.TypeA)
	if got := r.resolve(context.Background(), query, "udp"); len(got.Answer) != 1 {
		t.Fatalf("config not applied: %+v", got)
	}
	loaded, err := loadConfig(path)
	if err != nil || len(loaded.Records) != 1 {
		t.Fatalf("config not persisted: %+v, %v", loaded, err)
	}
	get := httptest.NewRecorder()
	api.ServeHTTP(get, httptest.NewRequest(http.MethodGet, "/api/state", nil))
	var state struct {
		Config Config `json:"config"`
	}
	if err := json.Unmarshal(get.Body.Bytes(), &state); err != nil || len(state.Config.Records) != 1 {
		t.Fatalf("unexpected state: %s %v", get.Body.String(), err)
	}
}

func TestInvalidConfigDoesNotReplaceSavedConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	r := newResolver(defaultConfig(), netip.MustParsePrefix("127.0.0.0/8"))
	api := (&app{resolver: r, path: path}).routes()
	request := httptest.NewRequest(http.MethodPut, "http://pilot.local/api/config", strings.NewReader(`{"upstream":"119.29.29.29","records":[{"name":"bad.home","type":"A","value":"not-an-ip"}]}`))
	request.Header.Set("X-Pilot-Request", "1")
	response := httptest.NewRecorder()
	api.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("expected validation failure: %d", response.Code)
	}
	if len(r.getConfig().Records) != 0 {
		t.Fatal("invalid config was applied")
	}
}
