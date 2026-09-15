package monitor

import (
	"bufio"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Interface struct {
	Name      string   `json:"name"`
	MAC       string   `json:"mac"`
	Addresses []string `json:"addresses"`
	State     string   `json:"state"`
	RX        uint64   `json:"rxBytes"`
	TX        uint64   `json:"txBytes"`
	RXRate    float64  `json:"rxRate"`
	TXRate    float64  `json:"txRate"`
}
type Point struct {
	Time int64   `json:"time"`
	CPU  float64 `json:"cpu"`
	RX   float64 `json:"rx"`
	TX   float64 `json:"tx"`
}
type Snapshot struct {
	Time          int64       `json:"time"`
	CPU           float64     `json:"cpu"`
	MemoryTotal   uint64      `json:"memoryTotal"`
	MemoryUsed    uint64      `json:"memoryUsed"`
	Uptime        float64     `json:"uptime"`
	Load          string      `json:"load"`
	Interfaces    []Interface `json:"interfaces"`
	History       []Point     `json:"history"`
	Warnings      []string    `json:"warnings"`
	SampleSeconds float64     `json:"sampleSeconds"`
}
type raw struct {
	total, idle                  uint64
	memoryTotal, memoryAvailable uint64
	uptime                       float64
	load                         string
	interfaces                   []Interface
	warnings                     []string
}
type Collector struct {
	mu       sync.RWMutex
	snapshot Snapshot
	err      error
	ready    bool
}

func ParseCPU(s string) (uint64, uint64, error) {
	fields := strings.Fields(strings.SplitN(s, "\n", 2)[0])
	if len(fields) < 5 || fields[0] != "cpu" {
		return 0, 0, fmt.Errorf("invalid CPU counters")
	}
	var total, idle uint64
	for i := 1; i < len(fields) && i <= 8; i++ {
		n, e := strconv.ParseUint(fields[i], 10, 64)
		if e != nil {
			return 0, 0, e
		}
		total += n
		if i == 4 || i == 5 {
			idle += n
		}
	}
	return total, idle, nil
}
func ParseMemory(s string) (uint64, uint64, error) {
	values := map[string]uint64{}
	scanner := bufio.NewScanner(strings.NewReader(s))
	for scanner.Scan() {
		f := strings.Fields(scanner.Text())
		if len(f) >= 2 {
			v, e := strconv.ParseUint(f[1], 10, 64)
			if e == nil {
				values[strings.TrimSuffix(f[0], ":")] = v * 1024
			}
		}
	}
	total := values["MemTotal"]
	avail, ok := values["MemAvailable"]
	if total == 0 || !ok || avail > total {
		return 0, 0, fmt.Errorf("invalid memory counters")
	}
	return total, avail, nil
}
func Rate(current, previous uint64, seconds float64) float64 {
	if current < previous || seconds <= 0 {
		return 0
	}
	return float64(current-previous) / seconds
}
func readRaw() (raw, error) {
	r := raw{interfaces: []Interface{}, warnings: []string{}}
	b, e := os.ReadFile("/proc/stat")
	if e != nil {
		return r, e
	}
	r.total, r.idle, e = ParseCPU(string(b))
	if e != nil {
		return r, e
	}
	b, e = os.ReadFile("/proc/meminfo")
	if e != nil {
		return r, e
	}
	r.memoryTotal, r.memoryAvailable, e = ParseMemory(string(b))
	if e != nil {
		return r, e
	}
	b, e = os.ReadFile("/proc/uptime")
	if e != nil {
		return r, e
	}
	f := strings.Fields(string(b))
	if len(f) == 0 {
		return r, fmt.Errorf("missing uptime")
	}
	r.uptime, e = strconv.ParseFloat(f[0], 64)
	if e != nil {
		return r, e
	}
	b, e = os.ReadFile("/proc/loadavg")
	if e == nil {
		v := strings.Fields(string(b))
		if len(v) >= 3 {
			r.load = strings.Join(v[:3], " / ")
		}
	}
	counters := map[string][2]uint64{}
	b, e = os.ReadFile("/proc/net/dev")
	if e != nil {
		r.warnings = append(r.warnings, "无法读取网卡流量计数")
	} else {
		for _, line := range strings.Split(string(b), "\n") {
			parts := strings.SplitN(line, ":", 2)
			if len(parts) != 2 {
				continue
			}
			v := strings.Fields(parts[1])
			if len(v) < 16 {
				continue
			}
			rx, er := strconv.ParseUint(v[0], 10, 64)
			tx, et := strconv.ParseUint(v[8], 10, 64)
			if er == nil && et == nil {
				counters[strings.TrimSpace(parts[0])] = [2]uint64{rx, tx}
			}
		}
	}
	interfaces, e := net.Interfaces()
	if e != nil {
		return r, e
	}
	for _, n := range interfaces {
		if n.Flags&net.FlagLoopback != 0 {
			continue
		}
		addresses := []string{}
		a, er := n.Addrs()
		if er != nil {
			r.warnings = append(r.warnings, "无法读取接口地址: "+n.Name)
		}
		for _, addr := range a {
			addresses = append(addresses, addr.String())
		}
		state := "unknown"
		if b, er := os.ReadFile("/sys/class/net/" + n.Name + "/operstate"); er == nil {
			state = strings.TrimSpace(string(b))
		}
		c := counters[n.Name]
		r.interfaces = append(r.interfaces, Interface{Name: n.Name, MAC: n.HardwareAddr.String(), Addresses: addresses, State: state, RX: c[0], TX: c[1]})
	}
	return r, nil
}
func New() *Collector { c := &Collector{}; go c.run(); return c }
func (c *Collector) Snapshot() (Snapshot, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.err != nil {
		return Snapshot{}, c.err
	}
	if !c.ready {
		return Snapshot{}, fmt.Errorf("等待首次采样")
	}
	return c.snapshot, nil
}
func (c *Collector) run() {
	previous, prevErr := readRaw()
	last := time.Now()
	history := []Point{}
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for now := range ticker.C {
		current, err := readRaw()
		seconds := now.Sub(last).Seconds()
		if err != nil {
			c.mu.Lock()
			c.err = err
			c.mu.Unlock()
			prevErr = err
			continue
		}
		if prevErr != nil {
			previous = current
			last = now
			prevErr = nil
			continue
		}
		cpu := 0.0
		if current.total > previous.total && current.idle >= previous.idle {
			dt := current.total - previous.total
			di := current.idle - previous.idle
			if di <= dt {
				cpu = 100 * float64(dt-di) / float64(dt)
			}
		}
		old := map[string]Interface{}
		for _, n := range previous.interfaces {
			old[n.Name] = n
		}
		rx, tx := 0.0, 0.0
		for i := range current.interfaces {
			n := &current.interfaces[i]
			if p, ok := old[n.Name]; ok {
				n.RXRate = Rate(n.RX, p.RX, seconds)
				n.TXRate = Rate(n.TX, p.TX, seconds)
			}
			rx += n.RXRate
			tx += n.TXRate
		}
		history = append(history, Point{Time: now.UnixMilli(), CPU: cpu, RX: rx, TX: tx})
		if len(history) > 120 {
			history = history[len(history)-120:]
		}
		snap := Snapshot{Time: now.UnixMilli(), CPU: cpu, MemoryTotal: current.memoryTotal, MemoryUsed: current.memoryTotal - current.memoryAvailable, Uptime: current.uptime, Load: current.load, Interfaces: current.interfaces, History: append([]Point{}, history...), Warnings: current.warnings, SampleSeconds: seconds}
		c.mu.Lock()
		c.snapshot = snap
		c.err = nil
		c.ready = true
		c.mu.Unlock()
		previous = current
		last = now
	}
}
