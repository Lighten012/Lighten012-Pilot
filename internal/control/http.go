package control

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/url"
	"time"
)

type Server struct{ client *http.Client }

func New(socket string) *Server {
	return &Server{client: &http.Client{Timeout: 110 * time.Second, Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{Timeout: 3 * time.Second}).DialContext(ctx, "unix", socket)
	}}}}
}
func reply(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}
func fail(w http.ResponseWriter, status int, msg string) {
	reply(w, status, map[string]string{"error": msg})
}
func mutationAllowed(r *http.Request) bool {
	if r.Header.Get("X-Pilot-Request") != "1" {
		return false
	}
	u, e := url.Parse(r.Header.Get("Origin"))
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return e == nil && u.Scheme == scheme && u.Host == r.Host && u.Path == "" && u.RawQuery == "" && u.Fragment == "" && u.User == nil
}

func (s *Server) Register(mux *http.ServeMux) {
	for _, route := range []struct{ method, path string }{{"GET", "logs"}, {"GET", "state"}, {"POST", "preview"}, {"POST", "draft"}, {"POST", "apply"}, {"POST", "confirm"}, {"POST", "rollback"}, {"GET", "services/state"}, {"POST", "services/preview"}, {"POST", "services/apply"}, {"GET", "firewall/state"}, {"POST", "firewall/preview"}, {"POST", "firewall/apply"}, {"POST", "firewall/confirm"}, {"POST", "firewall/rollback"}} {
		path := "/api/network/" + route.path
		if route.path == "logs" {
			path = "/api/logs"
		}
		mux.HandleFunc(route.method+" "+path, func(w http.ResponseWriter, r *http.Request) {
			if r.Method != "GET" && !mutationAllowed(r) {
				fail(w, 403, "请求来源无效")
				return
			}
			body, e := io.ReadAll(http.MaxBytesReader(w, r.Body, 65536))
			if e != nil {
				fail(w, 413, "请求过大")
				return
			}
			target := url.URL{Scheme: "http", Host: "unix", Path: "/" + route.path, RawQuery: r.URL.RawQuery}
			req, e := http.NewRequestWithContext(r.Context(), r.Method, target.String(), bytes.NewReader(body))
			if e != nil {
				fail(w, 500, "无法创建管理请求")
				return
			}
			req.Header.Set("Content-Type", "application/json")
			res, e := s.client.Do(req)
			if e != nil {
				fail(w, 503, "网络管理服务暂不可用，请稍后重试")
				return
			}
			defer res.Body.Close()
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.Header().Set("Cache-Control", "no-store")
			w.WriteHeader(res.StatusCode)
			io.Copy(w, io.LimitReader(res.Body, 8*1024*1024))
		})
	}
}
