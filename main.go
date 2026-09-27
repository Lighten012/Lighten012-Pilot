package main

import (
	"embed"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"
)

//go:embed web/*
var assets embed.FS

type app struct {
	resolver *Resolver
	network  *networkManager
	mihomo   *mihomoClient
	path     string
	saveMu   sync.Mutex
}

func (a *app) routes() http.Handler {
	mux := http.NewServeMux()
	if a.mihomo != nil {
		a.mihomo.routes(mux)
	}
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
	if a.network != nil {
		mux.HandleFunc("GET /api/lan/devices", func(w http.ResponseWriter, r *http.Request) {
			devices, err := a.network.lanDevices()
			if err != nil {
				log.Printf("LAN devices: %v", err)
				http.Error(w, "无法读取 LAN 设备", http.StatusInternalServerError)
				return
			}
			writeJSON(w, http.StatusOK, devices)
		})
		mux.HandleFunc("GET /api/network", func(w http.ResponseWriter, r *http.Request) {
			state, err := a.network.state()
			if err != nil {
				log.Printf("network state: %v", err)
				http.Error(w, "无法读取网卡", http.StatusInternalServerError)
				return
			}
			writeJSON(w, http.StatusOK, state)
		})
		mux.HandleFunc("PUT /api/network/roles", func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("X-Pilot-Request") != "1" || !sameOrigin(r) {
				http.Error(w, "请求来源无效", http.StatusForbidden)
				return
			}
			var roles networkRoles
			if err := decodeRequest(w, r, &roles); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			if err := a.network.selectRoles(roles); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			state, _ := a.network.state()
			writeJSON(w, http.StatusOK, state)
		})
		mux.HandleFunc("PUT /api/network/lan-ip", func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("X-Pilot-Request") != "1" || !sameOrigin(r) {
				http.Error(w, "请求来源无效", http.StatusForbidden)
				return
			}
			var input struct {
				Address string `json:"address"`
			}
			if err := decodeRequest(w, r, &input); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			if err := a.network.changeLANAddress(input.Address); err != nil {
				log.Printf("change LAN address: %v", err)
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			state, _ := a.network.state()
			writeJSON(w, http.StatusOK, state)
		})
	}
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
	webFiles, _ := fs.Sub(assets, "web")
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(http.FS(webFiles))))
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

func decodeRequest(w http.ResponseWriter, r *http.Request, v any) error {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(v); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return errors.New("只能提交一个 JSON 对象")
	}
	return nil
}

func main() {
	webAddr := flag.String("web", "0.0.0.0:80", "web listen address")
	dnsPort := flag.Int("dns-port", 53, "LAN DNS port")
	dhcpPort := flag.Int("dhcp-port", 67, "LAN DHCP server port")
	configPath := flag.String("config", "config.json", "persistent configuration path")
	dhcpLeases := flag.String("dhcp-leases", "dhcp-leases.json", "persistent DHCP leases")
	networkConfig := flag.String("network-config", "network.json", "saved WAN/LAN interface roles")
	interfacesDir := flag.String("interfaces-dir", "/etc/network/interfaces.d", "ifupdown interface files")
	mihomoConfig := flag.String("mihomo-config", "/var/lib/lighten012-pilot-v2/mihomo.yaml", "mihomo configuration file")
	flag.Parse()
	if *dnsPort < 1 || *dnsPort > 65535 {
		log.Fatal("invalid DNS port")
	}
	if *dhcpPort < 1 || *dhcpPort > 65535 {
		log.Fatal("invalid DHCP port")
	}
	c, err := loadConfig(*configPath)
	if err != nil {
		log.Fatalf("load config: %v", err)
	}
	r := newResolver(c)
	proxy, err := newProxyWhitelist(*mihomoConfig + ".whitelist.json")
	if err != nil {
		log.Fatalf("load proxy whitelist: %v", err)
	}
	r.proxy = proxy
	mihomo := newMihomoClient(*mihomoConfig)
	mihomo.whitelist = proxy
	network, err := newNetworkManager(*networkConfig, *interfacesDir, *webAddr)
	if err != nil {
		log.Fatalf("load network roles: %v", err)
	}
	dnsService := newDNSService(r, *dnsPort)
	network.dns = dnsService
	dhcpService := newDHCPService(*dhcpLeases, *dhcpPort)
	network.dhcp = dhcpService
	r.ipForMAC = dhcpService.ipForMAC
	forwarding, err := newForwarder()
	if err != nil {
		log.Fatalf("read IPv4 forwarding: %v", err)
	}
	network.forward = forwarding
	forwarding.proxy = proxy
	if network.roles != nil {
		lan, err := currentIPv4(network.roles.LAN)
		if err != nil {
			log.Fatalf("LAN DNS address: %v", err)
		}
		change, err := dnsService.stage(lan)
		if err != nil {
			log.Fatal(err)
		}
		dhcpChange, err := dhcpService.stage(network.roles.LAN, lan)
		if err != nil {
			change.abort()
			log.Fatalf("enable LAN DHCP: %v", err)
		}
		if err := forwarding.apply(*network.roles, lan); err != nil {
			dhcpChange.abort()
			change.abort()
			_ = forwarding.close()
			log.Fatalf("enable LAN forwarding: %v", err)
		}
		change.commit()
		dhcpChange.commit()
	}
	web := &http.Server{Addr: *webAddr, Handler: (&app{resolver: r, network: network, mihomo: mihomo, path: *configPath}).routes(), ReadHeaderTimeout: 5 * time.Second}
	errCh := make(chan error, 1)
	go func() { errCh <- web.ListenAndServe() }()
	log.Printf("Pilot v2 web http://%s; DNS follows selected LAN", *webAddr)
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
	dnsService.close()
	dhcpService.close()
	if err := forwarding.close(); err != nil {
		log.Printf("stop LAN forwarding: %v", err)
	}
}
