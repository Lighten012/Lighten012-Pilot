package main

import (
	"embed"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"log"
	"net/http"
	"net/netip"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/miekg/dns"
)

//go:embed web/*
var assets embed.FS

type app struct {
	resolver *Resolver
	path     string
	saveMu   sync.Mutex
}

func (a *app) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/state", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, struct {
			Config Config `json:"config"`
			Stats  Stats  `json:"stats"`
		}{a.resolver.getConfig(), a.resolver.stats()})
	})
	mux.HandleFunc("GET /api/interfaces", func(w http.ResponseWriter, r *http.Request) {
		interfaces, err := listInterfaces()
		if err != nil {
			log.Printf("list interfaces: %v", err)
			http.Error(w, "无法读取网卡", http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, interfaces)
	})
	mux.HandleFunc("PUT /api/config", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Pilot-Request") != "1" || !sameOrigin(r) {
			http.Error(w, "请求来源无效", http.StatusForbidden)
			return
		}
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 128<<10))
		dec.DisallowUnknownFields()
		var c Config
		if err := dec.Decode(&c); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err := dec.Decode(new(any)); err != io.EOF {
			http.Error(w, "只能提交一个 JSON 对象", http.StatusBadRequest)
			return
		}
		c, err := normalize(c)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		a.saveMu.Lock()
		defer a.saveMu.Unlock()
		if err := saveConfig(a.path, c); err != nil {
			log.Printf("save config: %v", err)
			http.Error(w, "保存失败", http.StatusInternalServerError)
			return
		}
		a.resolver.setConfig(c)
		writeJSON(w, http.StatusOK, c)
	})
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		b, _ := assets.ReadFile("web/index.html")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(b)
	})
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(http.FS(assets))))
	return mux
}

func sameOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	return origin == "http://"+r.Host || origin == "https://"+r.Host
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func main() {
	webAddr := flag.String("web", "127.0.0.1:8080", "web listen address")
	dnsAddr := flag.String("dns", "127.0.0.1:53", "DNS listen address")
	lan := flag.String("lan", "127.0.0.0/8", "allowed DNS client subnet")
	configPath := flag.String("config", "config.json", "persistent configuration path")
	flag.Parse()
	allowed, err := netip.ParsePrefix(*lan)
	if err != nil {
		log.Fatal(err)
	}
	c, err := loadConfig(*configPath)
	if err != nil {
		log.Fatalf("load config: %v", err)
	}
	r := newResolver(c, allowed)
	udp := &dns.Server{Addr: *dnsAddr, Net: "udp", Handler: r}
	tcp := &dns.Server{Addr: *dnsAddr, Net: "tcp", Handler: r}
	web := &http.Server{Addr: *webAddr, Handler: (&app{resolver: r, path: *configPath}).routes(), ReadHeaderTimeout: 5 * time.Second}
	errCh := make(chan error, 3)
	go func() { errCh <- udp.ListenAndServe() }()
	go func() { errCh <- tcp.ListenAndServe() }()
	go func() { errCh <- web.ListenAndServe() }()
	log.Printf("Pilot v2 DNS %s (clients %s), web http://%s", *dnsAddr, *lan, *webAddr)
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGTERM, syscall.SIGINT)
	select {
	case s := <-signals:
		log.Printf("stopping: %s", s)
	case e := <-errCh:
		if !errors.Is(e, http.ErrServerClosed) {
			log.Printf("server stopped: %v", e)
		}
	}
	_ = web.Close()
	_ = udp.Shutdown()
	_ = tcp.Shutdown()
}
