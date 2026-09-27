package main

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/miekg/dns"
)

func TestDNSUpstreamValidationAndPersistence(t *testing.T) {
	for _, c := range []Config{{}, {Upstream: "bad"}, {BackupUpstream: "bad"}} {
		if _, err := normalize(c); err == nil {
			t.Fatalf("accepted invalid upstreams: %+v", c)
		}
	}
	path := filepath.Join(t.TempDir(), "config.json")
	config, err := normalize(Config{BackupUpstream: "1.1.1.1", Records: []Record{}})
	if err != nil {
		t.Fatal(err)
	}
	if err := saveConfig(path, config); err != nil {
		t.Fatal(err)
	}
	loaded, err := loadConfig(path)
	if err != nil || loaded.Upstream != "" || loaded.BackupUpstream != "1.1.1.1" {
		t.Fatalf("unexpected saved config: %+v %v", loaded, err)
	}
}

func TestDNSConfigAPIRequiresAtLeastOneUpstream(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	r := newResolver(defaultConfig())
	api := (&app{resolver: r, path: path}).routes()
	request := httptest.NewRequest(http.MethodPut, "http://pilot.local/api/config", strings.NewReader(`{"upstream":"","backupUpstream":"","records":[]}`))
	request.Header.Set("X-Pilot-Request", "1")
	response := httptest.NewRecorder()
	api.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest || r.getConfig().Upstream != "119.29.29.29" {
		t.Fatalf("invalid config applied: %d %s", response.Code, response.Body.String())
	}
}

func TestDNSFallbackOnlyOnUpstreamFailure(t *testing.T) {
	config := Config{Upstream: "192.0.2.1", BackupUpstream: "192.0.2.2", Records: []Record{}}
	r := newResolver(config)
	var calls []string
	r.exchange = func(_ context.Context, q *dns.Msg, _, address string) (*dns.Msg, error) {
		calls = append(calls, address)
		if address == "192.0.2.1:53" {
			return nil, errors.New("unreachable")
		}
		answer := new(dns.Msg)
		answer.SetReply(q)
		answer.Answer = []dns.RR{&dns.A{Hdr: dns.RR_Header{Name: q.Question[0].Name, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 60}, A: net.ParseIP("203.0.113.7").To4()}}
		return answer, nil
	}
	query := new(dns.Msg)
	query.SetQuestion("example.org.", dns.TypeA)
	answer := r.resolve(context.Background(), query, "udp")
	if !reflect.DeepEqual(calls, []string{"192.0.2.1:53", "192.0.2.2:53"}) || len(answer.Answer) != 1 || answer.Answer[0].(*dns.A).A.String() != "203.0.113.7" {
		t.Fatalf("fallback failed: %v %+v", calls, answer)
	}
	calls = nil
	r.exchange = func(_ context.Context, q *dns.Msg, _, address string) (*dns.Msg, error) {
		calls = append(calls, address)
		answer := new(dns.Msg)
		answer.SetRcode(q, dns.RcodeNameError)
		return answer, nil
	}
	answer = r.resolve(context.Background(), query, "udp")
	if answer.Rcode != dns.RcodeNameError || !reflect.DeepEqual(calls, []string{"192.0.2.1:53"}) {
		t.Fatalf("NXDOMAIN should not fall back: %v %+v", calls, answer)
	}
}
