package main

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

type networkRoles struct {
	WAN string `json:"wan"`
	LAN string `json:"lan"`
}

type networkState struct {
	Interfaces []interfaceInfo `json:"interfaces"`
	Roles      *networkRoles   `json:"roles"`
	LANAddress string          `json:"lanAddress"`
}

type networkManager struct {
	mu            sync.Mutex
	roles         *networkRoles
	path          string
	interfacesDir string
	webAddress    string
	runIP         func(...string) error
}

var interfaceNamePattern = regexp.MustCompile(`^[a-zA-Z0-9_.:-]+$`)

func newNetworkManager(path, interfacesDir, webAddress string) (*networkManager, error) {
	m := &networkManager{path: path, interfacesDir: interfacesDir, webAddress: webAddress, runIP: runIPCommand}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return m, nil
	}
	if err != nil {
		return nil, err
	}
	var roles networkRoles
	if err := json.Unmarshal(data, &roles); err != nil {
		return nil, err
	}
	if roles.WAN == "" || roles.LAN == "" || roles.WAN == roles.LAN {
		return nil, errors.New("保存的 WAN/LAN 选择无效")
	}
	m.roles = &roles
	return m, nil
}

func (m *networkManager) state() (networkState, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	interfaces, err := listInterfaces()
	if err != nil {
		return networkState{}, err
	}
	state := networkState{Interfaces: interfaces}
	if m.roles != nil {
		roles := *m.roles
		state.Roles = &roles
		if lan, err := currentIPv4(m.roles.LAN); err == nil {
			state.LANAddress = lan.String()
		}
	}
	return state, nil
}

func (m *networkManager) selectRoles(roles networkRoles) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.roles != nil {
		return errors.New("WAN/LAN 网卡已经保存")
	}
	if roles.WAN == roles.LAN || !interfaceNamePattern.MatchString(roles.WAN) || !interfaceNamePattern.MatchString(roles.LAN) {
		return errors.New("请选择两张不同的网卡")
	}
	for _, name := range []string{roles.WAN, roles.LAN} {
		iface, err := net.InterfaceByName(name)
		if err != nil || iface.Flags&net.FlagLoopback != 0 {
			return fmt.Errorf("网卡 %s 不存在或不可选", name)
		}
	}
	if err := m.checkProtectedLAN(roles.LAN); err != nil {
		return err
	}
	lan, err := currentIPv4(roles.LAN)
	if err != nil {
		return err
	}
	if lan.Bits() < 8 || lan.Bits() > 30 {
		return errors.New("当前 LAN 前缀不支持修改，请使用 /8 到 /30")
	}
	if _, _, err := m.readLANFile(roles.LAN, lan); err != nil {
		return err
	}
	data, err := json.MarshalIndent(roles, "", "  ")
	if err != nil {
		return err
	}
	if err := writeAtomic(m.path, append(data, '\n'), 0600); err != nil {
		return err
	}
	m.roles = &roles
	return nil
}

func (m *networkManager) changeLANAddress(value string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.roles == nil {
		return errors.New("请先选择 WAN/LAN 网卡")
	}
	if err := m.checkProtectedLAN(m.roles.LAN); err != nil {
		return err
	}
	oldAddress, err := currentIPv4(m.roles.LAN)
	if err != nil {
		return err
	}
	if oldAddress.Bits() < 8 || oldAddress.Bits() > 30 {
		return errors.New("当前 LAN 前缀不支持修改，请使用 /8 到 /30")
	}
	newIP, err := netip.ParseAddr(strings.TrimSpace(value))
	if err != nil || !newIP.Is4() || !newIP.IsGlobalUnicast() {
		return errors.New("请输入有效的 LAN IPv4 地址")
	}
	if newIP == oldAddress.Addr() {
		return nil
	}
	newAddress := netip.PrefixFrom(newIP, oldAddress.Bits())
	if !validHostAddress(newAddress) {
		return errors.New("LAN 地址不能是网段地址或广播地址")
	}
	wanAddress, err := currentIPv4(m.roles.WAN)
	if err == nil && newAddress.Masked().Overlaps(wanAddress.Masked()) {
		return errors.New("LAN 网段不能与 WAN 网段重叠")
	}
	path, oldFile, err := m.readLANFile(m.roles.LAN, oldAddress)
	if err != nil {
		return err
	}
	newFile := strings.Replace(string(oldFile), "address "+oldAddress.String(), "address "+newAddress.String(), 1)
	if newFile == string(oldFile) {
		return errors.New("找不到 LAN 配置中的原地址")
	}
	if err := m.runIP("-4", "address", "add", newAddress.String(), "dev", m.roles.LAN); err != nil {
		return fmt.Errorf("添加新地址失败：%w", err)
	}
	if err := writeAtomic(path, []byte(newFile), 0644); err != nil {
		removeErr := m.runIP("-4", "address", "del", newAddress.String(), "dev", m.roles.LAN)
		return errors.Join(fmt.Errorf("保存 LAN 地址失败：%w", err), removeErr)
	}
	if err := m.runIP("-4", "address", "del", oldAddress.String(), "dev", m.roles.LAN); err != nil {
		restoreErr := writeAtomic(path, oldFile, 0644)
		removeErr := m.runIP("-4", "address", "del", newAddress.String(), "dev", m.roles.LAN)
		return errors.Join(fmt.Errorf("移除原地址失败：%w", err), restoreErr, removeErr)
	}
	return nil
}

func (m *networkManager) checkProtectedLAN(name string) error {
	host, _, err := net.SplitHostPort(m.webAddress)
	if err != nil {
		return err
	}
	webIP := net.ParseIP(host)
	if webIP == nil || webIP.IsUnspecified() {
		return errors.New("管理页面必须绑定具体 IP，才能安全修改 LAN")
	}
	iface, err := net.InterfaceByName(name)
	if err != nil {
		return err
	}
	addresses, err := iface.Addrs()
	if err != nil {
		return err
	}
	for _, address := range addresses {
		if ipNet, ok := address.(*net.IPNet); ok && ipNet.IP.Equal(webIP) {
			return errors.New("管理页面所在网卡不能选作 LAN")
		}
	}
	return nil
}

func currentIPv4(name string) (netip.Prefix, error) {
	iface, err := net.InterfaceByName(name)
	if err != nil {
		return netip.Prefix{}, err
	}
	addresses, err := iface.Addrs()
	if err != nil {
		return netip.Prefix{}, err
	}
	var result netip.Prefix
	for _, address := range addresses {
		prefix, err := netip.ParsePrefix(address.String())
		if err != nil || !prefix.Addr().Is4() {
			continue
		}
		if result.IsValid() {
			return netip.Prefix{}, fmt.Errorf("网卡 %s 有多个 IPv4 地址，暂不支持修改", name)
		}
		result = prefix
	}
	if !result.IsValid() {
		return netip.Prefix{}, fmt.Errorf("网卡 %s 没有 IPv4 地址", name)
	}
	return result, nil
}

func validHostAddress(prefix netip.Prefix) bool {
	if prefix.Bits() > 30 {
		return true
	}
	ip := binary.BigEndian.Uint32(prefix.Addr().AsSlice())
	mask := ^uint32(0) << (32 - prefix.Bits())
	network := ip & mask
	return ip != network && ip != network|^mask
}

func (m *networkManager) readLANFile(name string, current netip.Prefix) (string, []byte, error) {
	path := filepath.Join(m.interfacesDir, "pilot-"+name)
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return "", nil, fmt.Errorf("找不到 LAN 的静态配置文件 %s", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", nil, err
	}
	content := string(data)
	if !strings.Contains(content, "iface "+name+" inet static") || strings.Count(content, "address ") != 1 || !strings.Contains(content, "address "+current.String()) {
		return "", nil, errors.New("LAN 配置与当前地址不一致，暂不修改")
	}
	return path, data, nil
}

func writeAtomic(path string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".pilot-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if err := file.Chmod(mode); err != nil {
		file.Close()
		return err
	}
	if _, err := file.Write(data); err != nil {
		file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), path)
}

func runIPCommand(args ...string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, "/usr/sbin/ip", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}
