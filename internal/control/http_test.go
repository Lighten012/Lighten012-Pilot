package control

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestLogQueryProxy(t *testing.T) {
	s := New("/unused")
	s.client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/logs" || r.URL.Query().Get("search") != "one & two" || r.URL.Query().Get("cursor") != "s=1;i=2" {
			t.Fatal(r.URL)
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"entries":[]}`)), Header: make(http.Header)}, nil
	})
	mux := http.NewServeMux()
	s.Register(mux)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "http://pilot.test/api/logs?search=one%20%26%20two&cursor=s%3D1%3Bi%3D2", nil))
	if w.Code != 200 {
		t.Fatal(w.Code)
	}
}
func TestDirectManagementAndOrigin(t *testing.T) {
	s := New("/unused")
	calls := 0
	s.client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("{}")), Header: make(http.Header)}, nil
	})
	mux := http.NewServeMux()
	s.Register(mux)
	for _, tc := range []struct {
		method, path, origin, header string
		want                         int
	}{
		{"GET", "state", "", "", 200}, {"GET", "services/state", "", "", 200},
		{"POST", "preview", "http://pilot.test", "1", 200}, {"POST", "apply", "http://pilot.test", "1", 200},
		{"POST", "services/preview", "http://pilot.test", "1", 200}, {"POST", "services/apply", "http://pilot.test", "1", 200},
		{"POST", "apply", "http://other.test", "1", 403}, {"POST", "services/apply", "http://pilot.test", "", 403},
	} {
		r := httptest.NewRequest(tc.method, "http://pilot.test/api/network/"+tc.path, strings.NewReader("{}"))
		r.Header.Set("Origin", tc.origin)
		r.Header.Set("X-Pilot-Request", tc.header)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		if w.Code != tc.want {
			t.Fatalf("%s: got %d want %d", tc.path, w.Code, tc.want)
		}
		if len(w.Result().Cookies()) != 0 {
			t.Fatal("unexpected session cookie")
		}
	}
	if calls != 6 {
		t.Fatal("unexpected forwarded requests", calls)
	}
}
