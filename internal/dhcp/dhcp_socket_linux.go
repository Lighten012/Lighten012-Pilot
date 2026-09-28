//go:build linux

package dhcp

import (
	"context"
	"net"
	"strconv"
	"syscall"

	"golang.org/x/sys/unix"
)

func openDHCPConn(iface string, port int) (net.PacketConn, error) {
	listen := net.ListenConfig{Control: func(_, _ string, raw syscall.RawConn) error {
		var optionErr error
		if err := raw.Control(func(fd uintptr) {
			optionErr = unix.SetsockoptString(int(fd), unix.SOL_SOCKET, unix.SO_BINDTODEVICE, iface)
			if optionErr == nil {
				optionErr = unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_BROADCAST, 1)
			}
		}); err != nil {
			return err
		}
		return optionErr
	}}
	return listen.ListenPacket(context.Background(), "udp4", net.JoinHostPort("0.0.0.0", strconv.Itoa(port)))
}
