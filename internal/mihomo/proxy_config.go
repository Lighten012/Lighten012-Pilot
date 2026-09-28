package mihomo

import (
	"errors"
	"fmt"

	"gopkg.in/yaml.v3"
)

// applyDeviceProxyConfig keeps subscription nodes and groups, while Pilot owns
// the two transparent listeners used by devices with proxying enabled.
func applyDeviceProxyConfig(content []byte, preferred string) ([]byte, string, error) {
	var config map[string]any
	if err := yaml.Unmarshal(content, &config); err != nil || config == nil {
		return nil, "", errors.New("无效的 Mihomo 配置")
	}
	groups, ok := config["proxy-groups"].([]any)
	if !ok || len(groups) == 0 {
		return nil, "", errors.New("订阅中没有代理组")
	}
	selected := ""
	for _, raw := range groups {
		group, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		name, _ := group["name"].(string)
		if name == "" || name == "GLOBAL" {
			continue
		}
		if selected == "" {
			selected = name
		}
		if name == preferred {
			selected = preferred
			break
		}
	}
	if selected == "" {
		return nil, "", errors.New("订阅中没有可用的代理组")
	}
	config["mode"] = "rule"
	config["rules"] = []string{"MATCH,DIRECT"}
	config["mixed-port"] = 0
	config["redir-port"] = 0
	config["tproxy-port"] = 0
	config["allow-lan"] = true
	config["bind-address"] = "*"
	config["external-controller"] = "127.0.0.1:9090"
	config["secret"] = ""
	config["dns"] = map[string]any{"enable": false}
	config["tun"] = map[string]any{"enable": false}
	config["listeners"] = []map[string]any{
		{"name": "pilot-full-device", "type": "redir", "port": 7894, "listen": "0.0.0.0", "proxy": selected},
		{"name": "pilot-full-device-udp", "type": "tproxy", "port": 7895, "listen": "0.0.0.0", "udp": true, "proxy": selected},
	}
	for _, key := range []string{"socks-port", "port", "external-ui", "external-ui-url", "external-controller-unix", "external-controller-pipe"} {
		delete(config, key)
	}
	result, err := yaml.Marshal(config)
	if err != nil {
		return nil, "", fmt.Errorf("编码 Mihomo 配置: %w", err)
	}
	return result, selected, validateMihomoConfig(result)
}
