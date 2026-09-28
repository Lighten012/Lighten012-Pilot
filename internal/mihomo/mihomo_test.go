package mihomo

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestValidateMihomoConfigKeepsPilotNetworking(t *testing.T) {
	base := "external-controller: 127.0.0.1:9090\nallow-lan: false\ndns:\n  enable: false\ntun:\n  enable: false\n"
	if err := validateMihomoConfig([]byte(base)); err != nil {
		t.Fatalf("valid config: %v", err)
	}
	for name, config := range map[string]string{
		"external controller": "external-controller: 0.0.0.0:9090\n",
		"dns conflict":        strings.Replace(base, "dns:\n  enable: false", "dns:\n  enable: true", 1),
		"tun routing":         strings.Replace(base, "tun:\n  enable: false", "tun:\n  enable: true", 1),
		"lan proxy":           strings.Replace(base, "allow-lan: false", "allow-lan: true", 1),
	} {
		t.Run(name, func(t *testing.T) {
			if err := validateMihomoConfig([]byte(config)); err == nil {
				t.Fatal("expected rejection")
			}
		})
	}
}

func TestNormalizeSubscriptionKeepsPilotNetworking(t *testing.T) {
	input := []byte("mixed-port: 7899\nallow-lan: true\nexternal-controller: 0.0.0.0:9090\ndns:\n  enable: true\ntun:\n  enable: true\nproxies:\n  - name: node-a\n    type: ss\n    server: example.com\n    port: 443\n    cipher: aes-128-gcm\n    password: secret\nproxy-groups:\n  - name: MAIN\n    type: select\n    proxies: [node-a, DIRECT]\nrules:\n  - MATCH,MAIN\n")
	content, err := normalizeSubscription(input)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateMihomoConfig(content); err != nil {
		t.Fatal(err)
	}
	for _, wanted := range []string{"127.0.0.1:9090", "node-a", "mixed-port: 0"} {
		if !strings.Contains(string(content), wanted) {
			t.Fatalf("missing %q in %s", wanted, content)
		}
	}
	if _, err := normalizeSubscription([]byte("cHJveGllczogW10=")); err == nil {
		t.Fatal("base64 subscription accepted")
	}
}

func TestMihomoStateAndGroupSelection(t *testing.T) {
	selected := ""
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/version":
			w.Write([]byte(`{"version":"v1.19.31"}`))
		case "/configs":
			w.Write([]byte(`{"mode":"rule"}`))
		case "/proxies":
			w.Write([]byte(`{"proxies":{"MAIN":{"type":"Selector","now":"DIRECT","all":["DIRECT","Node A"]},"Node A":{"type":"Shadowsocks"}}}`))
		case "/connections":
			w.Write([]byte(`{"connections":[{"metadata":{"host":"example.com","destinationIP":"1.1.1.1","destinationPort":"443","network":"tcp"},"rule":"MATCH","upload":12,"download":34}]}`))
		case "/traffic":
			w.Write([]byte(`{"up":2,"down":4,"upTotal":6,"downTotal":8}`))
		case "/proxies/MAIN":
			if r.Method != http.MethodPut {
				t.Errorf("unexpected method: %s", r.Method)
			}
			var body struct {
				Name string `json:"name"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			selected = body.Name
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected path: %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	m := &mihomoClient{baseURL: server.URL, client: server.Client()}
	state := m.state(context.Background())
	if !state.Running || state.Version != "v1.19.31" || state.Up != 2 || state.Connections != 1 || len(state.Groups) != 1 || state.Groups[0].Name != "MAIN" {
		t.Fatalf("unexpected state: %+v", state)
	}
	if err := m.selectProxy(context.Background(), "MAIN", "Node A"); err != nil {
		t.Fatal(err)
	}
	if selected != "Node A" {
		t.Fatalf("selected %q", selected)
	}
	if err := m.selectProxy(context.Background(), "MAIN", "Missing"); err == nil {
		t.Fatal("unknown node accepted")
	}
}
