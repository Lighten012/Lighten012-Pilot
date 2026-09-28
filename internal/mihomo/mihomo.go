package mihomo

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Lighten012/Lighten012-Pilot/internal/httpx"
	"github.com/Lighten012/Lighten012-Pilot/internal/network"

	"gopkg.in/yaml.v3"
)

type mihomoClient struct {
	baseURL    string
	configPath string
	client     *http.Client
	mu         sync.Mutex
	proxy      *network.DeviceProxy
}

type Client = mihomoClient

func NewClient(configPath string) *Client                         { return newMihomoClient(configPath) }
func (m *mihomoClient) SetDeviceProxy(proxy *network.DeviceProxy) { m.proxy = proxy }
func (m *mihomoClient) Routes(mux *http.ServeMux)                 { m.routes(mux) }

type mihomoGroup struct {
	Name    string   `json:"name"`
	Type    string   `json:"type"`
	Now     string   `json:"now"`
	Options []string `json:"options"`
}

type mihomoConnection struct {
	Host        string `json:"host"`
	Destination string `json:"destination"`
	Network     string `json:"network"`
	Rule        string `json:"rule"`
	Upload      int64  `json:"upload"`
	Download    int64  `json:"download"`
}

type mihomoState struct {
	Running          bool               `json:"running"`
	Version          string             `json:"version,omitempty"`
	Mode             string             `json:"mode,omitempty"`
	Up               int64              `json:"up"`
	Down             int64              `json:"down"`
	UpTotal          int64              `json:"upTotal"`
	DownTotal        int64              `json:"downTotal"`
	Connections      int                `json:"connections"`
	Groups           []mihomoGroup      `json:"groups"`
	Recent           []mihomoConnection `json:"recent"`
	Error            string             `json:"error,omitempty"`
	SubscriptionHost string             `json:"subscriptionHost,omitempty"`
}

func newMihomoClient(configPath string) *mihomoClient {
	transport := &http.Transport{DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{Timeout: 2 * time.Second}).DialContext(ctx, network, "127.0.0.1:9090")
	}}
	return &mihomoClient{baseURL: "http://127.0.0.1:9090", configPath: configPath, client: &http.Client{Transport: transport, Timeout: 8 * time.Second}}
}

func (m *mihomoClient) do(ctx context.Context, method, path string, body any, result any) error {
	var input io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return err
		}
		input = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, m.baseURL+path, input)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := m.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		message, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("mihomo API %d: %s", resp.StatusCode, strings.TrimSpace(string(message)))
	}
	if result != nil {
		return json.NewDecoder(io.LimitReader(resp.Body, 2<<20)).Decode(result)
	}
	return nil
}

func (m *mihomoClient) state(ctx context.Context) mihomoState {
	state := mihomoState{Groups: []mihomoGroup{}, Recent: []mihomoConnection{}}
	if saved, err := os.ReadFile(m.subscriptionPath()); err == nil {
		if parsed, err := url.Parse(strings.TrimSpace(string(saved))); err == nil {
			state.SubscriptionHost = parsed.Hostname()
		}
	}
	var version struct {
		Version string `json:"version"`
	}
	if err := m.do(ctx, "GET", "/version", nil, &version); err != nil {
		state.Error = "Mihomo 未运行或控制接口不可用"
		return state
	}
	state.Running, state.Version = true, version.Version
	var config struct {
		Mode string `json:"mode"`
	}
	_ = m.do(ctx, "GET", "/configs", nil, &config)
	state.Mode = config.Mode
	var proxies struct {
		Proxies map[string]struct {
			Type string   `json:"type"`
			Now  string   `json:"now"`
			All  []string `json:"all"`
		} `json:"proxies"`
	}
	if err := m.do(ctx, "GET", "/proxies", nil, &proxies); err == nil {
		for name, proxy := range proxies.Proxies {
			if proxy.Type == "Selector" || proxy.Type == "URLTest" || proxy.Type == "Fallback" || proxy.Type == "LoadBalance" {
				state.Groups = append(state.Groups, mihomoGroup{Name: name, Type: proxy.Type, Now: proxy.Now, Options: proxy.All})
			}
		}
		sort.Slice(state.Groups, func(i, j int) bool { return state.Groups[i].Name < state.Groups[j].Name })
	}
	var connections struct {
		Connections []struct {
			Metadata struct {
				Host            string `json:"host"`
				DestinationIP   string `json:"destinationIP"`
				DestinationPort string `json:"destinationPort"`
				Network         string `json:"network"`
			} `json:"metadata"`
			Rule     string `json:"rule"`
			Upload   int64  `json:"upload"`
			Download int64  `json:"download"`
		} `json:"connections"`
	}
	if err := m.do(ctx, "GET", "/connections", nil, &connections); err == nil {
		state.Connections = len(connections.Connections)
		for i, item := range connections.Connections {
			if i >= 20 {
				break
			}
			state.Recent = append(state.Recent, mihomoConnection{
				Host: item.Metadata.Host, Destination: net.JoinHostPort(item.Metadata.DestinationIP, item.Metadata.DestinationPort),
				Network: item.Metadata.Network, Rule: item.Rule, Upload: item.Upload, Download: item.Download,
			})
		}
	}
	// /traffic is a stream; take one sample and close it rather than keeping a browser connection open.
	trafficCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(trafficCtx, "GET", m.baseURL+"/traffic", nil)
	if resp, err := m.client.Do(req); err == nil {
		defer resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			var sample struct {
				Up        int64 `json:"up"`
				Down      int64 `json:"down"`
				UpTotal   int64 `json:"upTotal"`
				DownTotal int64 `json:"downTotal"`
			}
			if json.NewDecoder(bufio.NewReader(resp.Body)).Decode(&sample) == nil {
				state.Up, state.Down, state.UpTotal, state.DownTotal = sample.Up, sample.Down, sample.UpTotal, sample.DownTotal
			}
		}
	}
	return state
}

func (m *mihomoClient) subscriptionPath() string { return m.configPath + ".subscription" }

func normalizeSubscription(content []byte) ([]byte, error) {
	var config map[string]any
	if err := yaml.Unmarshal(content, &config); err != nil || config == nil {
		return nil, errors.New("订阅内容不是有效的 Mihomo YAML 配置")
	}
	if config["proxies"] == nil && config["proxy-providers"] == nil {
		return nil, errors.New("订阅中没有代理节点；请使用 Mihomo/Clash 格式的订阅链接")
	}
	config["mixed-port"] = 0
	config["allow-lan"] = false
	config["external-controller"] = "127.0.0.1:9090"
	config["secret"] = ""
	config["tun"] = map[string]any{"enable": false}
	config["dns"] = map[string]any{"enable": false}
	delete(config, "listeners")
	for _, key := range []string{"redir-port", "tproxy-port", "socks-port", "port", "external-ui", "external-ui-url", "external-controller-unix", "external-controller-pipe", "bind-address"} {
		delete(config, key)
	}
	if config["proxy-groups"] == nil {
		return nil, errors.New("订阅中没有代理组；请使用完整的 Mihomo/Clash 配置订阅")
	}
	result, err := yaml.Marshal(config)
	if err != nil {
		return nil, err
	}
	return result, validateMihomoConfig(result)
}

func downloadSubscription(ctx context.Context, rawURL string) ([]byte, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Hostname() == "" || parsed.User != nil {
		return nil, errors.New("请输入有效的 HTTP 或 HTTPS 订阅链接")
	}
	transport := &http.Transport{Proxy: nil, DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		ips, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
		if err != nil {
			return nil, err
		}
		for _, ip := range ips {
			if !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip == netip.MustParseAddr("169.254.169.254") {
				return nil, errors.New("订阅链接不能指向本机或内网地址")
			}
		}
		if len(ips) == 0 {
			return nil, errors.New("订阅域名没有 IP 地址")
		}
		return (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, network, net.JoinHostPort(ips[0].String(), port))
	}}
	client := &http.Client{Transport: transport, Timeout: 20 * time.Second, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return errors.New("订阅重定向次数过多")
		}
		if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
			return errors.New("订阅重定向协议无效")
		}
		return nil
	}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	// Some subscription services return a Base64 node list for the generic
	// mihomo user agent and a complete YAML configuration for Clash clients.
	req.Header.Set("User-Agent", "clash-verge/v2.0.0")
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("下载订阅失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("订阅服务器返回 HTTP %d", resp.StatusCode)
	}
	content, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20+1))
	if err != nil {
		return nil, err
	}
	if len(content) > 1<<20 {
		return nil, errors.New("订阅内容超过 1 MiB")
	}
	return content, nil
}

func (m *mihomoClient) applySubscription(ctx context.Context, rawURL string) error {
	content, err := downloadSubscription(ctx, rawURL)
	if err != nil {
		return err
	}
	config, err := normalizeSubscription(content)
	if err != nil {
		return err
	}
	selectedGroup := ""
	if m.proxy != nil {
		config, selectedGroup, err = applyDeviceProxyConfig(config, m.proxy.Group())
		if err != nil {
			return err
		}
	}
	if err := m.importConfig(ctx, config); err != nil {
		return err
	}
	if m.proxy != nil && selectedGroup != m.proxy.Group() {
		if err := m.proxy.SetGroup(selectedGroup); err != nil {
			return err
		}
	}
	path := m.subscriptionPath()
	temp, err := os.CreateTemp(filepath.Dir(path), "mihomo-subscription-*")
	if err != nil {
		return err
	}
	defer os.Remove(temp.Name())
	if err := temp.Chmod(0600); err != nil {
		temp.Close()
		return err
	}
	if _, err := temp.WriteString(rawURL + "\n"); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return os.Rename(temp.Name(), path)
}

func (m *mihomoClient) selectProxy(ctx context.Context, group, name string) error {
	var data struct {
		Proxies map[string]struct {
			Type string   `json:"type"`
			All  []string `json:"all"`
		} `json:"proxies"`
	}
	if err := m.do(ctx, "GET", "/proxies", nil, &data); err != nil {
		return err
	}
	selected, ok := data.Proxies[group]
	if !ok || selected.Type != "Selector" {
		return errors.New("代理组不存在或不可手动切换")
	}
	for _, option := range selected.All {
		if option == name {
			return m.do(ctx, "PUT", "/proxies/"+url.PathEscape(group), map[string]string{"name": name}, nil)
		}
	}
	return errors.New("节点不属于该代理组")
}

func (m *mihomoClient) reload(ctx context.Context) error {
	return m.do(ctx, "PUT", "/configs?force=true", map[string]string{"path": m.configPath}, nil)
}

func (m *mihomoClient) delay(ctx context.Context, name string) (int, error) {
	var data struct {
		Proxies map[string]json.RawMessage `json:"proxies"`
	}
	if err := m.do(ctx, "GET", "/proxies", nil, &data); err != nil {
		return 0, err
	}
	if _, ok := data.Proxies[name]; !ok {
		return 0, errors.New("节点不存在")
	}
	path := "/proxies/" + url.PathEscape(name) + "/delay?url=" + url.QueryEscape("https://www.gstatic.com/generate_204") + "&timeout=5000"
	var result struct {
		Delay int `json:"delay"`
	}
	if err := m.do(ctx, "GET", path, nil, &result); err != nil {
		return 0, err
	}
	return result.Delay, nil
}

func validateMihomoConfig(content []byte) error {
	var config map[string]any
	if err := yaml.Unmarshal(content, &config); err != nil {
		return fmt.Errorf("YAML 格式错误: %w", err)
	}
	if config == nil {
		return errors.New("配置不能为空")
	}
	if config["external-controller"] != "127.0.0.1:9090" {
		return errors.New("控制接口必须是 127.0.0.1:9090")
	}
	if secret, exists := config["secret"]; exists && secret != "" && secret != nil {
		return errors.New("Pilot 本机控制接口不使用密钥")
	}
	if tun, ok := config["tun"].(map[string]any); ok && tun["enable"] == true {
		return errors.New("不支持 TUN")
	}
	if dns, ok := config["dns"].(map[string]any); ok && dns["enable"] == true {
		return errors.New("Mihomo DNS 必须关闭；由 Pilot 提供 DNS")
	}
	for _, key := range []string{"mixed-port", "redir-port", "tproxy-port", "socks-port", "port"} {
		if value, ok := config[key].(int); ok && value != 0 {
			return fmt.Errorf("%s 必须关闭", key)
		}
	}
	listeners, exists := config["listeners"]
	if !exists {
		return nil
	} // bootstrap config before importing a subscription
	items, ok := listeners.([]any)
	if !ok || len(items) != 2 {
		return errors.New("只允许 Pilot 的 TCP/UDP 透明入口")
	}
	expected := []struct {
		name, typ    string
		port, fields int
	}{{"pilot-full-device", "redir", 7894, 5}, {"pilot-full-device-udp", "tproxy", 7895, 6}}
	group := ""
	for i, want := range expected {
		item, ok := items[i].(map[string]any)
		if !ok || len(item) != want.fields || item["name"] != want.name || item["type"] != want.typ || item["port"] != want.port || item["listen"] != "0.0.0.0" {
			return errors.New("无效的 Pilot 透明入口")
		}
		current, ok := item["proxy"].(string)
		if !ok || current == "" {
			return errors.New("透明入口需要代理组")
		}
		if i == 0 {
			group = current
		} else if group != current || item["udp"] != true {
			return errors.New("TCP/UDP 入口代理组不一致或 UDP 未启用")
		}
	}
	if config["allow-lan"] != true || config["bind-address"] != "*" {
		return errors.New("透明入口必须监听 LAN")
	}
	return nil
}

func (m *mihomoClient) importConfig(ctx context.Context, content []byte) error {
	if len(content) == 0 || len(content) > 1<<20 {
		return errors.New("配置文件大小须在 1 字节到 1 MiB 之间")
	}
	if err := validateMihomoConfig(content); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	dir := filepath.Dir(m.configPath)
	temp, err := os.CreateTemp(dir, "mihomo-check-*.yaml")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if err := temp.Chmod(0600); err != nil {
		temp.Close()
		return err
	}
	if _, err := temp.Write(content); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	checkCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	output, err := exec.CommandContext(checkCtx, "/usr/local/bin/mihomo", "-t", "-d", dir, "-f", tempPath).CombinedOutput()
	if err != nil {
		return fmt.Errorf("Mihomo 校验失败: %s", strings.TrimSpace(string(output)))
	}
	old, err := os.ReadFile(m.configPath)
	if err != nil {
		return err
	}
	if err := os.Rename(tempPath, m.configPath); err != nil {
		return err
	}
	if err := m.reload(ctx); err != nil {
		_ = os.WriteFile(m.configPath, old, 0600)
		_ = m.reload(ctx)
		return fmt.Errorf("应用配置失败，已恢复旧配置: %w", err)
	}
	return nil
}

func (m *mihomoClient) routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/mihomo", func(w http.ResponseWriter, r *http.Request) {
		httpx.WriteJSON(w, http.StatusOK, m.state(r.Context()))
	})
	if m.proxy != nil {
		mux.HandleFunc("GET /api/mihomo/devices", func(w http.ResponseWriter, r *http.Request) { httpx.WriteJSON(w, http.StatusOK, m.proxy.Devices()) })
		mux.HandleFunc("GET /api/mihomo/proxy-group", func(w http.ResponseWriter, r *http.Request) {
			httpx.WriteJSON(w, http.StatusOK, map[string]string{"group": m.proxy.Group()})
		})
	}
	mutate := func(w http.ResponseWriter, r *http.Request) bool {
		if r.Header.Get("X-Pilot-Request") == "1" && httpx.SameOrigin(r) {
			return true
		}
		http.Error(w, "请求来源无效", http.StatusForbidden)
		return false
	}
	if m.proxy != nil {
		mux.HandleFunc("PUT /api/mihomo/devices/{mac}", func(w http.ResponseWriter, r *http.Request) {
			if !mutate(w, r) {
				return
			}
			var input struct {
				Full bool `json:"full"`
			}
			if err := httpx.DecodeRequest(w, r, &input); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			if err := m.proxy.SetDevice(r.PathValue("mac"), input.Full); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			httpx.WriteJSON(w, http.StatusOK, m.proxy.Devices())
		})
		mux.HandleFunc("PUT /api/mihomo/proxy-group", func(w http.ResponseWriter, r *http.Request) {
			if !mutate(w, r) {
				return
			}
			var input struct {
				Group string `json:"group"`
			}
			if err := httpx.DecodeRequest(w, r, &input); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			current, err := os.ReadFile(m.configPath)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			updated, selected, err := applyDeviceProxyConfig(current, input.Group)
			if err != nil || selected != input.Group {
				http.Error(w, "请选择有效的代理组", http.StatusBadRequest)
				return
			}
			if err := m.importConfig(r.Context(), updated); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			if err := m.proxy.SetGroup(selected); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			httpx.WriteJSON(w, http.StatusOK, map[string]string{"group": selected})
		})
	}
	mux.HandleFunc("PUT /api/mihomo/group", func(w http.ResponseWriter, r *http.Request) {
		if !mutate(w, r) {
			return
		}
		var input struct {
			Group string `json:"group"`
			Name  string `json:"name"`
		}
		if err := httpx.DecodeRequest(w, r, &input); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err := m.selectProxy(r.Context(), input.Group, input.Name); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]bool{"ok": true})
	})
	mux.HandleFunc("POST /api/mihomo/reload", func(w http.ResponseWriter, r *http.Request) {
		if !mutate(w, r) {
			return
		}
		if err := m.reload(r.Context()); err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]bool{"ok": true})
	})
	mux.HandleFunc("POST /api/mihomo/delay", func(w http.ResponseWriter, r *http.Request) {
		if !mutate(w, r) {
			return
		}
		var input struct {
			Name string `json:"name"`
		}
		if err := httpx.DecodeRequest(w, r, &input); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		name := input.Name
		if name == "" {
			http.Error(w, "请选择节点", http.StatusBadRequest)
			return
		}
		delay, err := m.delay(r.Context(), name)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]int{"delay": delay})
	})
	mux.HandleFunc("POST /api/mihomo/subscription", func(w http.ResponseWriter, r *http.Request) {
		if !mutate(w, r) {
			return
		}
		var input struct {
			URL string `json:"url"`
		}
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192))
		if err := decoder.Decode(&input); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if len(input.URL) > 4096 || input.URL == "" {
			http.Error(w, "请输入订阅链接", http.StatusBadRequest)
			return
		}
		if err := m.applySubscription(r.Context(), strings.TrimSpace(input.URL)); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]bool{"ok": true})
	})
	mux.HandleFunc("POST /api/mihomo/subscription/refresh", func(w http.ResponseWriter, r *http.Request) {
		if !mutate(w, r) {
			return
		}
		saved, err := os.ReadFile(m.subscriptionPath())
		if err != nil {
			http.Error(w, "尚未保存订阅链接", http.StatusBadRequest)
			return
		}
		if err := m.applySubscription(r.Context(), strings.TrimSpace(string(saved))); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]bool{"ok": true})
	})
}
