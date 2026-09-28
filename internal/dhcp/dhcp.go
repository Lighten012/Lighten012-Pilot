package dhcp

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/netip"
	"os"
	"sort"
	"sync"
	"time"

	"github.com/Lighten012/Lighten012-Pilot/internal/storage"
)

const dhcpLeaseTime = 12 * time.Hour

type dhcpLease struct {
	MAC     string `json:"mac"`
	IP      string `json:"ip"`
	Expires int64  `json:"expires"`
}

type dhcpOfferState struct {
	IP      netip.Addr
	Expires time.Time
}

type dhcpConfig struct {
	Server netip.Addr
	Mask   [4]byte
	Start  uint32
	End    uint32
}

type dhcpServer struct {
	mu         sync.Mutex
	iface      string
	config     dhcpConfig
	conn       net.PacketConn
	leases     map[string]dhcpLease
	offers     map[string]dhcpOfferState
	declined   map[netip.Addr]time.Time
	path       string
	done       chan struct{}
	clientPort int
}

type dhcpService struct {
	mu         sync.Mutex
	active     *dhcpServer
	path       string
	port       int
	clientPort int
}

type dhcpChange struct {
	service *dhcpService
	server  *dhcpServer
	config  dhcpConfig
	fresh   bool
}

// Service owns DHCP leases and the listener on the selected LAN interface.
type Service = dhcpService

// Change stages a listener or subnet update until the network change commits.
type Change = dhcpChange

// Lease is a persistent MAC-to-address allocation.
type Lease = dhcpLease

func NewService(path string, port int) *Service { return newDHCPService(path, port) }

func (s *dhcpService) Stage(iface string, lan netip.Prefix) (*Change, error) {
	return s.stage(iface, lan)
}

func (c *dhcpChange) Commit()                                 { c.commit() }
func (c *dhcpChange) Abort()                                  { c.abort() }
func (s *dhcpService) Close()                                 { s.close() }
func (s *dhcpService) IPForMAC(mac string) (netip.Addr, bool) { return s.ipForMAC(mac) }
func (s *dhcpService) CurrentLeases() []Lease                 { return s.currentLeases() }

func PoolRange(lan netip.Prefix) (string, error) {
	pool, err := makeDHCPConfig(lan)
	if err != nil {
		return "", err
	}
	return dhcpAddr(pool.Start).String() + "–" + dhcpAddr(pool.End).String(), nil
}

func newDHCPService(path string, port int) *dhcpService {
	return &dhcpService{path: path, port: port, clientPort: 68}
}

func makeDHCPConfig(lan netip.Prefix) (dhcpConfig, error) {
	if !lan.IsValid() || !lan.Addr().Is4() || lan.Bits() < 8 || lan.Bits() > 30 {
		return dhcpConfig{}, errors.New("DHCP 需要 /8 到 /30 的 LAN IPv4 网段")
	}
	base := binary.BigEndian.Uint32(lan.Masked().Addr().AsSlice())
	mask := ^uint32(0) << (32 - lan.Bits())
	last := base | ^mask
	start, end := base+100, base+200
	if start >= last {
		start, end = base+1, last-1
	} else if end >= last {
		end = last - 1
	}
	server := binary.BigEndian.Uint32(lan.Addr().AsSlice())
	if server >= start && server <= end {
		if end-server >= server-start {
			start = server + 1
		} else {
			end = server - 1
		}
	}
	if start > end {
		return dhcpConfig{}, errors.New("LAN 网段没有可用于 DHCP 的地址")
	}
	var maskBytes [4]byte
	binary.BigEndian.PutUint32(maskBytes[:], mask)
	return dhcpConfig{Server: lan.Addr(), Mask: maskBytes, Start: start, End: end}, nil
}

func (c dhcpConfig) contains(ip netip.Addr) bool {
	if !ip.Is4() || ip == c.Server {
		return false
	}
	value := binary.BigEndian.Uint32(ip.AsSlice())
	return value >= c.Start && value <= c.End
}

func dhcpAddr(value uint32) netip.Addr {
	var bytes [4]byte
	binary.BigEndian.PutUint32(bytes[:], value)
	return netip.AddrFrom4(bytes)
}

func loadDHCPLeases(path string, config dhcpConfig) (map[string]dhcpLease, error) {
	leases := make(map[string]dhcpLease)
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return leases, nil
	}
	if err != nil {
		return nil, err
	}
	var saved []dhcpLease
	if err := json.Unmarshal(data, &saved); err != nil {
		return nil, err
	}
	now := time.Now().Unix()
	used := make(map[string]bool)
	for _, lease := range saved {
		ip, err := netip.ParseAddr(lease.IP)
		if _, macErr := net.ParseMAC(lease.MAC); err != nil || macErr != nil || !config.contains(ip) || lease.Expires <= now || used[lease.IP] {
			continue
		}
		used[lease.IP] = true
		leases[lease.MAC] = lease
	}
	return leases, nil
}

func (s *dhcpServer) saveLeases(leases map[string]dhcpLease) error {
	list := make([]dhcpLease, 0, len(leases))
	for _, lease := range leases {
		list = append(list, lease)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].MAC < list[j].MAC })
	data, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	return storage.WriteAtomic(s.path, append(data, '\n'), 0600)
}

func (s *dhcpService) stage(iface string, lan netip.Prefix) (*dhcpChange, error) {
	config, err := makeDHCPConfig(lan)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.active != nil {
		if s.active.iface != iface {
			return nil, errors.New("DHCP LAN 网卡不能更改")
		}
		return &dhcpChange{service: s, server: s.active, config: config}, nil
	}
	leases, err := loadDHCPLeases(s.path, config)
	if err != nil {
		return nil, fmt.Errorf("读取 DHCP 租约: %w", err)
	}
	conn, err := openDHCPConn(iface, s.port)
	if err != nil {
		return nil, fmt.Errorf("监听 LAN DHCP: %w", err)
	}
	server := &dhcpServer{
		iface: iface, config: config, conn: conn, leases: leases,
		offers: make(map[string]dhcpOfferState), declined: make(map[netip.Addr]time.Time),
		path: s.path, done: make(chan struct{}), clientPort: s.clientPort,
	}
	go server.serve()
	return &dhcpChange{service: s, server: server, config: config, fresh: true}, nil
}

func (c *dhcpChange) commit() {
	c.service.mu.Lock()
	defer c.service.mu.Unlock()
	if c.fresh {
		c.service.active = c.server
	} else {
		c.server.mu.Lock()
		c.server.config = c.config
		for mac, lease := range c.server.leases {
			ip, err := netip.ParseAddr(lease.IP)
			if err != nil || !c.config.contains(ip) {
				delete(c.server.leases, mac)
			}
		}
		clear(c.server.offers)
		clear(c.server.declined)
		if err := c.server.saveLeases(c.server.leases); err != nil {
			log.Printf("save DHCP leases after LAN change: %v", err)
		}
		c.server.mu.Unlock()
	}
	log.Printf("LAN DHCP on %s: %s-%s", c.server.iface, dhcpAddr(c.config.Start), dhcpAddr(c.config.End))
}

func (c *dhcpChange) abort() {
	if c.fresh {
		c.server.close()
	}
}

func (s *dhcpService) close() {
	s.mu.Lock()
	server := s.active
	s.active = nil
	s.mu.Unlock()
	if server != nil {
		server.close()
	}
}

// ipForMAC only trusts a current lease on the active LAN DHCP server.
func (s *dhcpService) ipForMAC(mac string) (netip.Addr, bool) {
	s.mu.Lock()
	server := s.active
	if server == nil {
		s.mu.Unlock()
		return netip.Addr{}, false
	}
	server.mu.Lock()
	s.mu.Unlock()
	defer server.mu.Unlock()
	lease, found := server.leases[mac]
	if found && lease.Expires > time.Now().Unix() {
		ip, err := netip.ParseAddr(lease.IP)
		if err == nil && server.config.contains(ip) {
			return ip, true
		}
	}
	return netip.Addr{}, false
}

func (s *dhcpService) currentLeases() []dhcpLease {
	s.mu.Lock()
	server := s.active
	if server == nil {
		s.mu.Unlock()
		return nil
	}
	server.mu.Lock()
	s.mu.Unlock()
	defer server.mu.Unlock()
	now := time.Now().Unix()
	leases := make([]dhcpLease, 0, len(server.leases))
	for _, lease := range server.leases {
		if lease.Expires > now {
			leases = append(leases, lease)
		}
	}
	return leases
}

func (s *dhcpServer) close() {
	_ = s.conn.Close()
	<-s.done
}

func (s *dhcpServer) serve() {
	defer close(s.done)
	buffer := make([]byte, 1500)
	for {
		count, _, err := s.conn.ReadFrom(buffer)
		if err != nil {
			if !errors.Is(err, net.ErrClosed) {
				log.Printf("LAN DHCP read: %v", err)
			}
			return
		}
		reply, destination := s.handle(buffer[:count])
		if reply != nil {
			if _, err := s.conn.WriteTo(reply, destination); err != nil {
				log.Printf("LAN DHCP reply: %v", err)
			}
		}
	}
}

func (s *dhcpServer) handle(data []byte) ([]byte, net.Addr) {
	request := parseDHCPPacket(data)
	if !request.valid || request.header[24] != 0 || request.header[25] != 0 || request.header[26] != 0 || request.header[27] != 0 {
		return nil, nil // No DHCP relays in this small LAN-only server.
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	for mac, offer := range s.offers {
		if now.After(offer.Expires) {
			delete(s.offers, mac)
		}
	}
	for ip, expiry := range s.declined {
		if now.After(expiry) {
			delete(s.declined, ip)
		}
	}
	config := s.config
	var replyType byte
	var address netip.Addr
	switch request.messageType {
	case dhcpDiscover:
		address = s.available(request.mac, request.requested, now)
		if !address.IsValid() {
			return nil, nil
		}
		s.offers[request.mac] = dhcpOfferState{IP: address, Expires: now.Add(time.Minute)}
		replyType = dhcpOffer
	case dhcpRequest:
		if request.serverID.IsValid() && request.serverID != config.Server {
			delete(s.offers, request.mac)
			return nil, nil
		}
		address = request.requested
		if !address.IsValid() || address.IsUnspecified() {
			address = request.clientIP
		}
		if !config.contains(address) || !s.freeFor(request.mac, address, now) {
			replyType = dhcpNAK
			address = netip.Addr{}
			break
		}
		proposed := make(map[string]dhcpLease, len(s.leases)+1)
		for mac, lease := range s.leases {
			if lease.Expires > now.Unix() {
				proposed[mac] = lease
			}
		}
		proposed[request.mac] = dhcpLease{MAC: request.mac, IP: address.String(), Expires: now.Add(dhcpLeaseTime).Unix()}
		if err := s.saveLeases(proposed); err != nil {
			log.Printf("save DHCP lease: %v", err)
			return nil, nil
		}
		s.leases = proposed
		delete(s.offers, request.mac)
		replyType = dhcpACK
	case dhcpRelease:
		if lease, ok := s.leases[request.mac]; ok && lease.IP == request.clientIP.String() {
			proposed := make(map[string]dhcpLease, len(s.leases))
			for mac, item := range s.leases {
				if mac != request.mac {
					proposed[mac] = item
				}
			}
			if err := s.saveLeases(proposed); err == nil {
				s.leases = proposed
			}
		}
		return nil, nil
	case dhcpDecline:
		if config.contains(request.requested) {
			s.declined[request.requested] = now.Add(10 * time.Minute)
			if lease, ok := s.leases[request.mac]; ok && lease.IP == request.requested.String() {
				proposed := make(map[string]dhcpLease, len(s.leases))
				for mac, item := range s.leases {
					if mac != request.mac {
						proposed[mac] = item
					}
				}
				if err := s.saveLeases(proposed); err == nil {
					s.leases = proposed
				}
			}
		}
		delete(s.offers, request.mac)
		return nil, nil
	default:
		return nil, nil
	}
	reply := buildDHCPReply(request, replyType, config.Server, address, config.Mask)
	destination := net.Addr(&net.UDPAddr{IP: net.IPv4bcast, Port: s.clientPort})
	if replyType == dhcpACK && request.clientIP.Is4() && !request.clientIP.IsUnspecified() {
		destination = &net.UDPAddr{IP: net.IP(request.clientIP.AsSlice()), Port: s.clientPort}
	}
	return reply, destination
}

func (s *dhcpServer) freeFor(mac string, ip netip.Addr, now time.Time) bool {
	if !s.config.contains(ip) {
		return false
	}
	if expiry, found := s.declined[ip]; found && now.Before(expiry) {
		return false
	}
	for other, lease := range s.leases {
		if other != mac && lease.IP == ip.String() && lease.Expires > now.Unix() {
			return false
		}
	}
	for other, offer := range s.offers {
		if other != mac && offer.IP == ip && now.Before(offer.Expires) {
			return false
		}
	}
	return true
}

func (s *dhcpServer) available(mac string, requested netip.Addr, now time.Time) netip.Addr {
	if lease, ok := s.leases[mac]; ok && lease.Expires > now.Unix() {
		if ip, err := netip.ParseAddr(lease.IP); err == nil && s.freeFor(mac, ip, now) {
			return ip
		}
	}
	if requested.IsValid() && s.freeFor(mac, requested, now) {
		return requested
	}
	for value := s.config.Start; value <= s.config.End; value++ {
		ip := dhcpAddr(value)
		if s.freeFor(mac, ip, now) {
			return ip
		}
	}
	return netip.Addr{}
}
