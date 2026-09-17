package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHealthAndAPIBoundary(t *testing.T) {
	h := newHandler(t.TempDir())
	r := httptest.NewRecorder()
	h.ServeHTTP(r, httptest.NewRequest("GET", "/api/health", nil))
	var body map[string]string
	if r.Code != 200 || json.Unmarshal(r.Body.Bytes(), &body) != nil || body["mode"] != "prototype" {
		t.Fatalf("invalid health: %s", r.Body.String())
	}
	for _, tc := range []struct {
		method, path string
		status       int
	}{{"POST", "/api/health", http.StatusNotFound}, {"POST", "/api/network/apply", http.StatusForbidden}, {"GET", "/api/missing", http.StatusNotFound}} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(tc.method, tc.path, nil))
		if w.Code != tc.status {
			t.Errorf("%s %s: %d", tc.method, tc.path, w.Code)
		}
	}
}
