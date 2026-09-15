package main

import (
	"encoding/json"
	"flag"
	"github.com/Lighten012/Lighten012-Pilot/internal/monitor"
	"log"
	"net/http"
	"os"
	"runtime"
	"time"
)

func newHandler(webDir string) http.Handler {
	mux := http.NewServeMux()
	collector := monitor.New()
	mux.HandleFunc("GET /api/monitor", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		snapshot, err := collector.Snapshot()
		if err != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			json.NewEncoder(w).Encode(map[string]string{"error": "系统采样暂不可用，请稍后重试"})
			return
		}
		json.NewEncoder(w).Encode(snapshot)
	})
	mux.HandleFunc("GET /api/health", func(w http.ResponseWriter, r *http.Request) {
		hostname, _ := os.Hostname()
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		json.NewEncoder(w).Encode(map[string]string{"status": "ok", "version": "0.1.0", "mode": "prototype", "hostname": hostname, "architecture": runtime.GOARCH, "os": runtime.GOOS})
	})
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "API not implemented", http.StatusNotFound)
	})
	files := http.FileServer(http.Dir(webDir))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" && r.Method != "HEAD" {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		files.ServeHTTP(w, r)
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "same-origin")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; object-src 'none'; frame-ancestors 'none'; base-uri 'self'")
		mux.ServeHTTP(w, r)
	})
}

func main() {
	listen := flag.String("listen", "127.0.0.1:8080", "HTTP listen address")
	web := flag.String("web", "web/dist", "Built frontend directory")
	flag.Parse()
	server := &http.Server{Addr: *listen, Handler: newHandler(*web), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 15 * time.Second, IdleTimeout: 60 * time.Second}
	log.Printf("Lighten012-Pilot prototype listening on %s", *listen)
	log.Fatal(server.ListenAndServe())
}
