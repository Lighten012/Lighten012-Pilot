package main

import (
	"encoding/binary"
	"net"
	"net/netip"
)

const (
	dhcpDiscover = 1
	dhcpOffer    = 2
	dhcpRequest  = 3
	dhcpDecline  = 4
	dhcpACK      = 5
	dhcpNAK      = 6
	dhcpRelease  = 7
)

type dhcpPacket struct {
	header      [236]byte
	mac         string
	messageType byte
	requested   netip.Addr
	serverID    netip.Addr
	clientIP    netip.Addr
	valid       bool
}

func parseDHCPPacket(data []byte) dhcpPacket {
	var packet dhcpPacket
	if len(data) < 240 || data[0] != 1 || data[1] != 1 || data[2] != 6 ||
		data[236] != 99 || data[237] != 130 || data[238] != 83 || data[239] != 99 {
		return packet
	}
	copy(packet.header[:], data[:236])
	packet.mac = net.HardwareAddr(data[28:34]).String()
	packet.clientIP = netip.AddrFrom4([4]byte(data[12:16]))
	for i := 240; i < len(data); {
		code := data[i]
		i++
		if code == 255 {
			break
		}
		if code == 0 {
			continue
		}
		if i >= len(data) || i+1+int(data[i]) > len(data) {
			return dhcpPacket{}
		}
		length := int(data[i])
		i++
		value := data[i : i+length]
		i += length
		switch code {
		case 53:
			if length == 1 {
				packet.messageType = value[0]
			}
		case 50:
			if length == 4 {
				packet.requested = netip.AddrFrom4([4]byte(value))
			}
		case 54:
			if length == 4 {
				packet.serverID = netip.AddrFrom4([4]byte(value))
			}
		}
	}
	packet.valid = packet.messageType != 0
	return packet
}

func appendDHCPOption(data []byte, code byte, value []byte) []byte {
	return append(append(data, code, byte(len(value))), value...)
}

func buildDHCPReply(request dhcpPacket, messageType byte, server, offered netip.Addr, mask [4]byte) []byte {
	response := make([]byte, 240, 320)
	response[0], response[1], response[2] = 2, 1, 6
	copy(response[4:12], request.header[4:12])
	copy(response[12:16], request.header[12:16])
	if messageType == dhcpNAK {
		clear(response[12:16])
	}
	copy(response[28:44], request.header[28:44])
	if offered.IsValid() && offered.Is4() {
		copy(response[16:20], offered.AsSlice())
	}
	copy(response[236:240], []byte{99, 130, 83, 99})
	response = appendDHCPOption(response, 53, []byte{messageType})
	response = appendDHCPOption(response, 54, server.AsSlice())
	if messageType == dhcpOffer || messageType == dhcpACK {
		lease := make([]byte, 4)
		binary.BigEndian.PutUint32(lease, 12*60*60)
		response = appendDHCPOption(response, 51, lease)
		response = appendDHCPOption(response, 1, mask[:])
		response = appendDHCPOption(response, 3, server.AsSlice())
		response = appendDHCPOption(response, 6, server.AsSlice())
		binary.BigEndian.PutUint32(lease, 6*60*60)
		response = appendDHCPOption(response, 58, lease)
		binary.BigEndian.PutUint32(lease, 10*60*60+30*60)
		response = appendDHCPOption(response, 59, lease)
	}
	response = append(response, 255)
	for len(response) < 300 {
		response = append(response, 0)
	}
	return response
}
