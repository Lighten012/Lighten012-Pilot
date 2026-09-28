package pilot

import (
	"encoding/json"
	"errors"
	"flag"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/Lighten012/Lighten012-Pilot/internal/dhcp"
	"github.com/Lighten012/Lighten012-Pilot/internal/dns"
	"github.com/Lighten012/Lighten012-Pilot/internal/httpx"
	"github.com/Lighten012/Lighten012-Pilot/internal/mihomo"
	"github.com/Lighten012/Lighten012-Pilot/internal/network"
)

type app struct {
	assets   fs.FS
	resolver *dnsservice.Resolver
	network  *network.Manager
	mihomo   *mihomo.Client
	path     string
	saveMu   sync.Mutex
}

func (a *app) routes() http.Handler {
	mux := http.NewServeMux()
	if a.mihomo != nil {
		a.mihomo.Routes(mux)
	}
	mux.HandleFunc("GET /api/state", func(w http.ResponseWriter, r *http.Request) {
		httpx.WriteJSON(w, http.StatusOK, struct {
			Config dnsservice.Config `json:"config"`
			Stats  dnsservice.Stats  `json:"stats"`
		}{a.resolver.GetConfig(), a.resolver.Stats()})
	})
	mux.HandleFunc("GET /api/interfaces", func(w http.ResponseWriter, r *http.Request) {
		interfaces, err := network.ListInterfaces()
		if err != nil {
			log.Printf("list interfaces: %v", err)
			http.Error(w, "无法读取网卡", http.StatusInternalServerError)
			return
		}
		httpx.WriteJSON(w, http.StatusOK, interfaces)
	})
	if a.network != nil {
		mux.HandleFunc("GET /api/lan/devices", func(w http.ResponseWriter, r *http.Request) {
			devices, err := a.network.LANDevices()
			if err != nil {
				log.Printf("LAN devices: %v", err)
				http.Error(w, "无法读取 LAN 设备", http.StatusInternalServerError)
				return
			}
			httpx.WriteJSON(w, http.StatusOK, devices)
		})
		mux.HandleFunc("GET /api/network", func(w http.ResponseWriter, r *http.Request) {
			state, err := a.network.State()
			if err != nil {
				log.Printf("network state: %v", err)
				http.Error(w, "无法读取网卡", http.StatusInternalServerError)
				return
			}
			httpx.WriteJSON(w, http.StatusOK, state)
		})
		mux.HandleFunc("PUT /api/network/roles", func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("X-Pilot-Request") != "1" || !httpx.SameOrigin(r) {
				http.Error(w, "请求来源无效", http.StatusForbidden)
				return
			}
			var roles network.Roles
			if err := httpx.DecodeRequest(w, r, &roles); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			if err := a.network.SelectRoles(roles); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			state, _ := a.network.State()
			httpx.WriteJSON(w, http.StatusOK, state)
		})
		mux.HandleFunc("PUT /api/network/lan-ip", func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("X-Pilot-Request") != "1" || !httpx.SameOrigin(r) {
				http.Error(w, "请求来源无效", http.StatusForbidden)
				return
			}
			var input struct {
				Address string `json:"address"`
			}
			if err := httpx.DecodeRequest(w, r, &input); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			if err := a.network.ChangeLANAddress(input.Address); err != nil {
				log.Printf("change LAN address: %v", err)
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			state, _ := a.network.State()
			httpx.WriteJSON(w, http.StatusOK, state)
		})
	}
	mux.HandleFunc("PUT /api/config", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Pilot-Request") != "1" || !httpx.SameOrigin(r) {
			http.Error(w, "请求来源无效", http.StatusForbidden)
			return
		}
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 128<<10))
		dec.DisallowUnknownFields()
		var c dnsservice.Config
		if err := dec.Decode(&c); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err := dec.Decode(new(any)); err != io.EOF {
			http.Error(w, "只能提交一个 JSON 对象", http.StatusBadRequest)
			return
		}
		c, err := dnsservice.Normalize(c)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		a.saveMu.Lock()
		defer a.saveMu.Unlock()
		if err := dnsservice.SaveConfig(a.path, c); err != nil {
			log.Printf("save config: %v", err)
			http.Error(w, "保存失败", http.StatusInternalServerError)
			return
		}
		a.resolver.SetConfig(c)
		httpx.WriteJSON(w, http.StatusOK, c)
	})
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		if a.assets == nil {
			http.NotFound(w, r)
			return
		}
		b, _ := fs.ReadFile(a.assets, "web/index.html")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(b)
	})
	if a.assets != nil {
		webFiles, _ := fs.Sub(a.assets, "web")
		mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(http.FS(webFiles))))
	}
	return mux
}

func Run(assets fs.FS) {
	webAddr := flag.String("web", "0.0.0.0:80", "web listen address")
	dnsPort := flag.Int("dns-port", 53, "LAN DNS port")
	dhcpPort := flag.Int("dhcp-port", 67, "LAN DHCP server port")
	configPath := flag.String("config", "config.json", "persistent configuration path")
	dhcpLeases := flag.String("dhcp-leases", "dhcp-leases.json", "persistent DHCP leases")
	networkConfig := flag.String("network-config", "network.json", "saved WAN/LAN interface roles")
	interfacesDir := flag.String("interfaces-dir", "/etc/network/interfaces.d", "ifupdown interface files")
	mihomoConfig := flag.String("mihomo-config", "/var/lib/lighten012-pilot/mihomo.yaml", "mihomo configuration file")
	flag.Parse()
	if *dnsPort < 1 || *dnsPort > 65535 {
		log.Fatal("invalid DNS port")
	}
	if *dhcpPort < 1 || *dhcpPort > 65535 {
		log.Fatal("invalid DHCP port")
	}
	c, err := dnsservice.LoadConfig(*configPath)
	if err != nil {
		log.Fatalf("load config: %v", err)
	}
	r := dnsservice.NewResolver(c)
	proxy, err := network.NewDeviceProxy(filepath.Join(filepath.Dir(*mihomoConfig), "device-proxy.json"), *mihomoConfig+".whitelist.json")
	if err != nil {
		log.Fatalf("load device proxy: %v", err)
	}
	mihomo := mihomo.NewClient(*mihomoConfig)
	mihomo.SetDeviceProxy(proxy)
	manager, err := network.NewManager(*networkConfig, *interfacesDir, *webAddr)
	if err != nil {
		log.Fatalf("load network roles: %v", err)
	}
	dnsService := dnsservice.NewService(r, *dnsPort)
	dhcpService := dhcp.NewService(*dhcpLeases, *dhcpPort)
	r.SetIPForMAC(dhcpService.IPForMAC)
	forwarding, err := network.NewForwarder()
	if err != nil {
		log.Fatalf("read IPv4 forwarding: %v", err)
	}
	manager.Attach(dnsService, dhcpService, forwarding)
	forwarding.SetProxy(proxy)
	if roles := manager.Roles(); roles != nil {
		lan, err := network.CurrentIPv4(roles.LAN)
		if err != nil {
			log.Fatalf("LAN DNS address: %v", err)
		}
		change, err := dnsService.Stage(lan)
		if err != nil {
			log.Fatal(err)
		}
		dhcpChange, err := dhcpService.Stage(roles.LAN, lan)
		if err != nil {
			change.Abort()
			log.Fatalf("enable LAN DHCP: %v", err)
		}
		if err := forwarding.Apply(*roles, lan); err != nil {
			dhcpChange.Abort()
			change.Abort()
			_ = forwarding.Close()
			log.Fatalf("enable LAN forwarding: %v", err)
		}
		change.Commit()
		dhcpChange.Commit()
	}
	web := &http.Server{Addr: *webAddr, Handler: (&app{assets: assets, resolver: r, network: manager, mihomo: mihomo, path: *configPath}).routes(), ReadHeaderTimeout: 5 * time.Second}
	errCh := make(chan error, 1)
	go func() { errCh <- web.ListenAndServe() }()
	log.Printf("Pilot web http://%s; DNS follows selected LAN", *webAddr)
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
	dnsService.Close()
	dhcpService.Close()
	if err := forwarding.Close(); err != nil {
		log.Printf("stop LAN forwarding: %v", err)
	}
}
