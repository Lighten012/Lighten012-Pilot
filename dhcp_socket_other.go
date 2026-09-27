//go:build !linux

package main

import (
	"errors"
	"net"
)

func openDHCPConn(_ string, _ int) (net.PacketConn, error) {
	return nil, errors.New("DHCP 服务仅支持 Linux")
}
