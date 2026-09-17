package services

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/Lighten012/Lighten012-Pilot/internal/network"
	"github.com/miekg/dns"
)

type Runtime struct {
	resolver *Resolver
	servers  []*dns.Server
	cmd      *exec.Cmd
	done     chan struct{}
	alive    atomic.Bool
	mu       sync.Mutex
	failure  string
}

func (r *Runtime) fail(e error) {
	if e != nil {
		r.mu.Lock()
		r.failure = e.Error()
		r.mu.Unlock()
	}
}
func (r *Runtime) Error() string {
	if r == nil {
		return ""
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.failure
}
func (r *Runtime) Stop() {
	if r == nil {
		return
	}
	for _, s := range r.servers {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = s.ShutdownContext(ctx)
		cancel()
	}
	if r.cmd != nil && r.alive.Load() {
		_ = r.cmd.Process.Signal(syscall.SIGTERM)
		select {
		case <-r.done:
		case <-time.After(3 * time.Second):
			_ = r.cmd.Process.Kill()
			<-r.done
		}
	}
}
func DHCPText(c Config, dir string) string {
	p, _ := netip.ParsePrefix(c.Address)
	mask := net.IP(net.CIDRMask(p.Bits(), 32)).String()
	ip := p.Addr().String()
	return fmt.Sprintf("port=0\nno-hosts\nno-resolv\ninterface=%s\nexcept-interface=lo\nbind-interfaces\ndhcp-range=%s,%s,%s,%dm\ndhcp-option=option:router,%s\ndhcp-option=option:dns-server,%s\ndhcp-leasefile=%s\ndhcp-lease-max=4096\npid-file=\nlog-facility=-\n", c.Interface, c.DHCP.Start, c.DHCP.End, mask, c.DHCP.LeaseMinutes, ip, ip, filepath.Join(dir, "dhcp.leases"))
}
func Start(c Config, dir string) (r *Runtime, err error) {
	r = &Runtime{}
	defer func() {
		if err != nil {
			r.fail(err)
			r.Stop()
		}
	}()
	if c.DNS.Enabled {
		r.resolver = NewResolver(c)
		lc := net.ListenConfig{Control: func(_, _ string, raw syscall.RawConn) error {
			var e error
			ce := raw.Control(func(fd uintptr) {
				e = syscall.SetsockoptString(int(fd), syscall.SOL_SOCKET, syscall.SO_BINDTODEVICE, c.Interface)
			})
			if ce != nil {
				return ce
			}
			return e
		}}
		p, _ := netip.ParsePrefix(c.Address)
		addr := net.JoinHostPort(p.Addr().String(), "53")
		udp, e := lc.ListenPacket(context.Background(), "udp4", addr)
		if e != nil {
			return r, fmt.Errorf("DNS UDP 监听失败: %w", e)
		}
		tcp, e := lc.Listen(context.Background(), "tcp4", addr)
		if e != nil {
			udp.Close()
			return r, fmt.Errorf("DNS TCP 监听失败: %w", e)
		}
		for _, s := range []*dns.Server{{PacketConn: udp, Handler: r.resolver, UDPSize: 1232}, {Listener: tcp, Handler: r.resolver, ReadTimeout: 5 * time.Second, WriteTimeout: 5 * time.Second, MaxTCPQueries: 32}} {
			started := make(chan struct{})
			failed := make(chan error, 1)
			s.NotifyStartedFunc = func() { close(started) }
			go func(s *dns.Server) { e := s.ActivateAndServe(); r.fail(e); failed <- e }(s)
			select {
			case <-started:
				r.servers = append(r.servers, s)
			case e := <-failed:
				udp.Close()
				tcp.Close()
				return r, fmt.Errorf("DNS 启动失败: %v", e)
			case <-time.After(3 * time.Second):
				udp.Close()
				tcp.Close()
				return r, fmt.Errorf("DNS 启动超时")
			}
		}
	}
	if c.DHCP.Enabled {
		path := filepath.Join(dir, "dnsmasq.conf")
		if e := network.AtomicFile(path, []byte(DHCPText(c, dir)), 0600); e != nil {
			return r, e
		}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		out, e := exec.CommandContext(ctx, "/usr/sbin/dnsmasq", "--test", "--conf-file="+path).CombinedOutput()
		cancel()
		if e != nil {
			return r, fmt.Errorf("DHCP 配置检查失败: %s", out)
		}
		// No shell or system-wide dnsmasq configuration. The child dies with netd,
		// even though netd's unit preserves unrelated ifupdown DHCP clients.
		cmd := exec.Command("/usr/sbin/dnsmasq", "--keep-in-foreground", "--user=root", "--group=root", "--conf-file="+path)
		cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGTERM}
		cmd.Stderr = os.Stderr
		cmd.Stdout = os.Stdout
		if e = cmd.Start(); e != nil {
			return r, e
		}
		r.cmd = cmd
		r.done = make(chan struct{})
		r.alive.Store(true)
		go func() {
			e := cmd.Wait()
			r.alive.Store(false)
			if e != nil {
				r.fail(fmt.Errorf("DHCP 进程退出: %w", e))
			} else {
				r.fail(fmt.Errorf("DHCP 进程已停止"))
			}
			close(r.done)
		}()
		select {
		case <-r.done:
			return r, fmt.Errorf("DHCP 启动失败: %s", r.Error())
		case <-time.After(350 * time.Millisecond):
		}
	}
	return r, nil
}
