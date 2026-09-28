package dnsservice

import (
	"fmt"
	"log"
	"net"
	"net/netip"
	"strconv"
	"sync"

	"github.com/miekg/dns"
)

// dnsService binds only the selected LAN address. Each listener carries its
// own client subnet, so a pending LAN address change does not alter the old
// listener until the network change has succeeded.
type dnsService struct {
	mu       sync.Mutex
	resolver *Resolver
	port     int
	active   *dnsPair
}

type dnsPair struct {
	address string
	udp     *dns.Server
	tcp     *dns.Server
}

type dnsChange struct {
	service *dnsService
	pair    *dnsPair
	noop    bool
}

type Service = dnsService
type Change = dnsChange

func NewService(resolver *Resolver, port int) *Service        { return newDNSService(resolver, port) }
func (s *dnsService) Stage(lan netip.Prefix) (*Change, error) { return s.stage(lan) }
func (c *dnsChange) Commit()                                  { c.commit() }
func (c *dnsChange) Abort()                                   { c.abort() }
func (s *dnsService) Close()                                  { s.close() }

type lanDNSHandler struct {
	resolver *Resolver
	allowed  netip.Prefix
}

func (h lanDNSHandler) ServeDNS(w dns.ResponseWriter, q *dns.Msg) {
	h.resolver.serveDNS(w, q, h.allowed)
}

func newDNSService(resolver *Resolver, port int) *dnsService {
	return &dnsService{resolver: resolver, port: port}
}

func (s *dnsService) stage(lan netip.Prefix) (*dnsChange, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	address := net.JoinHostPort(lan.Addr().String(), strconv.Itoa(s.port))
	if s.active != nil && s.active.address == address {
		return &dnsChange{service: s, noop: true}, nil
	}
	udpConn, err := net.ListenPacket("udp4", address)
	if err != nil {
		return nil, fmt.Errorf("监听 LAN DNS UDP %s: %w", address, err)
	}
	tcpListener, err := net.Listen("tcp4", address)
	if err != nil {
		udpConn.Close()
		return nil, fmt.Errorf("监听 LAN DNS TCP %s: %w", address, err)
	}
	handler := lanDNSHandler{resolver: s.resolver, allowed: lan.Masked()}
	pair := &dnsPair{
		address: address,
		udp:     &dns.Server{PacketConn: udpConn, Net: "udp", Handler: handler},
		tcp:     &dns.Server{Listener: tcpListener, Net: "tcp", Handler: handler},
	}
	go func() {
		if err := pair.udp.ActivateAndServe(); err != nil {
			log.Printf("LAN DNS UDP %s stopped: %v", address, err)
		}
	}()
	go func() {
		if err := pair.tcp.ActivateAndServe(); err != nil {
			log.Printf("LAN DNS TCP %s stopped: %v", address, err)
		}
	}()
	return &dnsChange{service: s, pair: pair}, nil
}

func (c *dnsChange) commit() {
	if c.noop {
		return
	}
	c.service.mu.Lock()
	old := c.service.active
	c.service.active = c.pair
	c.service.mu.Unlock()
	if old != nil {
		old.close()
	}
	log.Printf("LAN DNS listening on %s", c.pair.address)
}

func (c *dnsChange) abort() {
	if !c.noop {
		c.pair.close()
	}
}

func (p *dnsPair) close() {
	_ = p.udp.Shutdown()
	_ = p.tcp.Shutdown()
}

func (s *dnsService) close() {
	s.mu.Lock()
	pair := s.active
	s.active = nil
	s.mu.Unlock()
	if pair != nil {
		pair.close()
	}
}
