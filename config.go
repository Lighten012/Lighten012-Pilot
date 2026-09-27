package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strings"

	"github.com/miekg/dns"
)

type Record struct {
	Name  string `json:"name"`
	Type  string `json:"type"`
	Value string `json:"value"`
	TTL   uint32 `json:"ttl"`
	MAC   string `json:"mac,omitempty"`
}

type Config struct {
	Upstream string   `json:"upstream"`
	Records  []Record `json:"records"`
}

func defaultConfig() Config { return Config{Upstream: "119.29.29.29", Records: []Record{}} }

func normalize(c Config) (Config, error) {
	c.Upstream = strings.TrimSpace(c.Upstream)
	if ip := net.ParseIP(c.Upstream); ip == nil || ip.To4() == nil {
		return c, errors.New("上游 DNS 必须是 IPv4 地址")
	}
	if len(c.Records) > 500 {
		return c, errors.New("最多保存 500 条记录")
	}
	seen := map[string]bool{}
	for i := range c.Records {
		r := &c.Records[i]
		r.Name = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(r.Name), "."))
		r.Type = strings.ToUpper(strings.TrimSpace(r.Type))
		r.Value = strings.TrimSpace(r.Value)
		r.MAC = strings.TrimSpace(r.MAC)
		if r.MAC != "" {
			mac, err := net.ParseMAC(r.MAC)
			if err != nil || len(mac) != 6 {
				return c, fmt.Errorf("第 %d 条记录的 MAC 地址无效", i+1)
			}
			r.MAC = mac.String()
		}
		if !validName(r.Name) {
			return c, fmt.Errorf("第 %d 条记录的域名无效", i+1)
		}
		ip, err := netip.ParseAddr(r.Value)
		if err != nil || (r.Type != "A" && r.Type != "AAAA") || (r.Type == "A" && !ip.Is4()) || (r.Type == "AAAA" && !ip.Is6()) {
			return c, fmt.Errorf("第 %d 条记录的类型或 IP 地址无效", i+1)
		}
		r.Value = ip.String()
		if r.TTL == 0 {
			r.TTL = 60
		}
		if r.TTL > 86400 {
			return c, fmt.Errorf("第 %d 条记录的 TTL 不能超过 86400", i+1)
		}
		key := r.Name + "/" + r.Type + "/" + r.MAC
		if seen[key] {
			return c, fmt.Errorf("重复记录：%s", key)
		}
		seen[key] = true
	}
	return c, nil
}

func validName(name string) bool {
	if len(name) == 0 || len(name) > 253 {
		return false
	}
	for _, label := range strings.Split(name, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, ch := range label {
			if !(ch >= 'a' && ch <= 'z' || ch >= '0' && ch <= '9' || ch == '-') {
				return false
			}
		}
	}
	return true
}

func loadConfig(path string) (Config, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return defaultConfig(), nil
	}
	if err != nil {
		return Config{}, err
	}
	var c Config
	if err := json.Unmarshal(b, &c); err != nil {
		return c, err
	}
	return normalize(c)
}

func saveConfig(path string, c Config) error {
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".config-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err := f.Chmod(0600); err != nil {
		f.Close()
		return err
	}
	if _, err := f.Write(append(b, '\n')); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

func recordRR(r Record) dns.RR {
	h := dns.RR_Header{Name: dns.Fqdn(r.Name), Class: dns.ClassINET, Ttl: r.TTL}
	if r.Type == "A" {
		h.Rrtype = dns.TypeA
		return &dns.A{Hdr: h, A: net.ParseIP(r.Value).To4()}
	}
	h.Rrtype = dns.TypeAAAA
	return &dns.AAAA{Hdr: h, AAAA: net.ParseIP(r.Value)}
}
