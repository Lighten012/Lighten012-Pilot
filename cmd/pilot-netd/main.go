package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"github.com/Lighten012/Lighten012-Pilot/internal/firewall"
	"github.com/Lighten012/Lighten012-Pilot/internal/network"
	"github.com/Lighten012/Lighten012-Pilot/internal/services"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

func decode(w http.ResponseWriter, r *http.Request, v any) error {
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 65536))
	d.DisallowUnknownFields()
	if e := d.Decode(v); e != nil {
		return e
	}
	if e := d.Decode(&struct{}{}); e != io.EOF {
		return fmt.Errorf("多余 JSON 内容")
	}
	return nil
}
func main() {
	socket := flag.String("socket", "/run/pilot-netd/control.sock", "Unix socket")
	state := flag.String("state", "/var/lib/pilot-netd", "Journal directory")
	root := flag.String("network-root", "/etc/network", "ifupdown directory")
	protected := flag.String("protected", "", "Comma-separated management interfaces")
	ifstate := flag.String("ifstate-dir", "/run/network", "ifupdown runtime state directory")
	timeout := flag.Duration("confirm-timeout", 90*time.Second, "Confirmation window")
	flag.Parse()
	locked := map[string]bool{}
	for _, n := range strings.Split(*protected, ",") {
		if n != "" {
			locked[n] = true
		}
	}
	m, e := network.NewManager(&network.Linux{Root: *root, StateDir: *ifstate, Protected: locked}, *state, *timeout)
	if e != nil {
		log.Fatal(e)
	}
	if e = m.Recover(); e != nil {
		log.Printf("Recovery requires retry: %v", e)
	}
	sm, e := services.NewManager(filepath.Join(*state, "services"), m.Status)
	if e != nil {
		log.Fatal(e)
	}
	var operations sync.Mutex
	fm, e := firewall.NewManager(&firewall.Linux{}, filepath.Join(*state, "firewall"), m.Status, *timeout)
	if e != nil {
		log.Fatal(e)
	}
	go func() {
		for range time.Tick(time.Second) {
			m.Tick()
			sm.Reconcile()
			fm.Tick()
		}
	}()
	mux := http.NewServeMux()
	reply := func(w http.ResponseWriter, v any, e error) {
		w.Header().Set("Content-Type", "application/json")
		if e != nil {
			w.WriteHeader(409)
			v = map[string]string{"error": e.Error()}
		}
		json.NewEncoder(w).Encode(v)
	}
	mux.HandleFunc("GET /state", func(w http.ResponseWriter, r *http.Request) { s, e := m.Status(); reply(w, s, e) })
	mux.HandleFunc("POST /preview", func(w http.ResponseWriter, r *http.Request) {
		var c network.Config
		if e := decode(w, r, &c); e != nil {
			reply(w, nil, e)
			return
		}
		p, e := m.Preview(c)
		reply(w, p, e)
	})
	mux.HandleFunc("POST /draft", func(w http.ResponseWriter, r *http.Request) {
		var c network.Config
		if e := decode(w, r, &c); e != nil {
			reply(w, nil, e)
			return
		}
		reply(w, map[string]bool{"ok": true}, m.Save(c))
	})
	mux.HandleFunc("POST /apply", func(w http.ResponseWriter, r *http.Request) {
		operations.Lock()
		defer operations.Unlock()
		if fm.Enabled() {
			reply(w, nil, fmt.Errorf("修改 WAN/LAN 前，请先停用防火墙与 NAT 并确认"))
			return
		}
		if sm.Enabled() {
			reply(w, nil, fmt.Errorf("修改 WAN/LAN 前，请先停用 DHCP 和 DNS；网络确认后再重新启用"))
			return
		}
		var req network.ApplyRequest
		if e := decode(w, r, &req); e != nil {
			reply(w, nil, e)
			return
		}
		id, e := m.Apply(req)
		reply(w, map[string]string{"id": id}, e)
	})
	mux.HandleFunc("GET /services/state", func(w http.ResponseWriter, r *http.Request) { s, e := sm.State(); reply(w, s, e) })
	mux.HandleFunc("POST /services/preview", func(w http.ResponseWriter, r *http.Request) {
		var c services.Config
		if e := decode(w, r, &c); e != nil {
			reply(w, nil, e)
			return
		}
		p, e := sm.Preview(c)
		reply(w, p, e)
	})
	mux.HandleFunc("POST /services/apply", func(w http.ResponseWriter, r *http.Request) {
		operations.Lock()
		defer operations.Unlock()
		var req services.ApplyRequest
		if e := decode(w, r, &req); e != nil {
			reply(w, nil, e)
			return
		}
		reply(w, map[string]bool{"ok": true}, sm.Apply(req))
	})
	for _, action := range []string{"confirm", "rollback"} {
		mux.HandleFunc("POST /"+action, func(w http.ResponseWriter, r *http.Request) {
			var req struct {
				ID string `json:"id"`
			}
			if e := decode(w, r, &req); e != nil {
				reply(w, nil, e)
				return
			}
			var e error
			if action == "confirm" {
				e = m.Confirm(req.ID)
			} else {
				e = m.Rollback(req.ID)
			}
			reply(w, map[string]bool{"ok": true}, e)
		})
	}
	mux.HandleFunc("GET /firewall/state", func(w http.ResponseWriter, r *http.Request) { s, e := fm.State(); reply(w, s, e) })
	mux.HandleFunc("POST /firewall/preview", func(w http.ResponseWriter, r *http.Request) {
		var c firewall.Config
		if e := decode(w, r, &c); e != nil {
			reply(w, nil, e)
			return
		}
		p, e := fm.Preview(c)
		reply(w, p, e)
	})
	mux.HandleFunc("POST /firewall/apply", func(w http.ResponseWriter, r *http.Request) {
		operations.Lock()
		defer operations.Unlock()
		var p firewall.Plan
		if e := decode(w, r, &p); e != nil {
			reply(w, nil, e)
			return
		}
		id, e := fm.Apply(p)
		reply(w, map[string]string{"id": id}, e)
	})
	for _, action := range []string{"confirm", "rollback"} {
		mux.HandleFunc("POST /firewall/"+action, func(w http.ResponseWriter, r *http.Request) {
			var req struct {
				ID string `json:"id"`
			}
			if e := decode(w, r, &req); e != nil {
				reply(w, nil, e)
				return
			}
			var e error
			if action == "confirm" {
				e = fm.Confirm(req.ID)
			} else {
				e = fm.Rollback(req.ID)
			}
			reply(w, map[string]bool{"ok": true}, e)
		})
	}
	if e = os.MkdirAll(filepathDir(*socket), 0750); e != nil {
		log.Fatal(e)
	}
	if e = os.Remove(*socket); e != nil && !os.IsNotExist(e) {
		log.Fatal(e)
	}
	listener, e := net.Listen("unix", *socket)
	if e != nil {
		log.Fatal(e)
	}
	if e = os.Chmod(*socket, 0660); e != nil {
		log.Fatal(e)
	}
	log.Printf("Network helper listening on Unix socket %s", *socket)
	log.Fatal((&http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 120 * time.Second}).Serve(listener))
}
func filepathDir(p string) string {
	i := strings.LastIndex(p, "/")
	if i < 0 {
		return "."
	}
	return p[:i]
}
