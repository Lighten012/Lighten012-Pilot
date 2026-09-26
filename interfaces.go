package main

import "net"

type interfaceInfo struct {
	Index     int      `json:"index"`
	Name      string   `json:"name"`
	MAC       string   `json:"mac"`
	Up        bool     `json:"up"`
	Running   bool     `json:"running"`
	Loopback  bool     `json:"loopback"`
	Addresses []string `json:"addresses"`
}

func listInterfaces() ([]interfaceInfo, error) {
	interfaces, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	result := make([]interfaceInfo, 0, len(interfaces))
	for _, iface := range interfaces {
		addresses, err := iface.Addrs()
		if err != nil {
			return nil, err
		}
		info := interfaceInfo{
			Index:     iface.Index,
			Name:      iface.Name,
			MAC:       iface.HardwareAddr.String(),
			Up:        iface.Flags&net.FlagUp != 0,
			Running:   iface.Flags&net.FlagRunning != 0,
			Loopback:  iface.Flags&net.FlagLoopback != 0,
			Addresses: make([]string, 0, len(addresses)),
		}
		for _, address := range addresses {
			info.Addresses = append(info.Addresses, address.String())
		}
		result = append(result, info)
	}
	return result, nil
}
