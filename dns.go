package main

import (
	"context"
	"net"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/miekg/dns"
)

type Stats struct {
	Queries   uint64 `json:"queries"`
	Local     uint64 `json:"local"`
	Forwarded uint64 `json:"forwarded"`
	Mihomo    uint64 `json:"mihomo"`
	Failed    uint64 `json:"failed"`
}

type Resolver struct {
	mu                                        sync.RWMutex
	config                                    Config
	records                                   map[string][]dns.RR
	macRecords                                map[string]Record
	ipForMAC                                  func(string) (netip.Addr, bool)
	proxy                                     *proxyWhitelist
	mihomoDNSAddr                             string
	exchange                                  func(context.Context, *dns.Msg, string, string) (*dns.Msg, error)
	slots                                     chan struct{}
	queries, local, forwarded, mihomo, failed atomic.Uint64
}

func newResolver(c Config) *Resolver {
	r := &Resolver{slots: make(chan struct{}, 128), mihomoDNSAddr: "127.0.0.1:1053"}
	r.setConfig(c)
	return r
}

func (r *Resolver) setConfig(c Config) {
	records := make(map[string][]dns.RR)
	macRecords := make(map[string]Record)
	for _, v := range c.Records {
		if v.MAC == "" {
			records[v.Name] = append(records[v.Name], recordRR(v))
		} else {
			macRecords[v.Name] = v
		}
	}
	r.mu.Lock()
	r.config = c
	r.records = records
	r.macRecords = macRecords
	r.mu.Unlock()
}

func (r *Resolver) getConfig() Config {
	r.mu.RLock()
	defer r.mu.RUnlock()
	c := r.config
	c.Records = append([]Record{}, c.Records...)
	return c
}

func (r *Resolver) stats() Stats {
	return Stats{r.queries.Load(), r.local.Load(), r.forwarded.Load(), r.mihomo.Load(), r.failed.Load()}
}

func reply(q *dns.Msg, code int) *dns.Msg {
	m := new(dns.Msg)
	m.SetRcode(q, code)
	m.RecursionAvailable = true
	return m
}

func (r *Resolver) resolve(ctx context.Context, q *dns.Msg, transport string) *dns.Msg {
	r.queries.Add(1)
	if q.Response || q.Opcode != dns.OpcodeQuery || len(q.Question) != 1 || q.Question[0].Qclass != dns.ClassINET {
		return reply(q, dns.RcodeFormatError)
	}
	question := q.Question[0]
	if question.Qtype == dns.TypeAXFR || question.Qtype == dns.TypeIXFR || q.IsTsig() != nil {
		return reply(q, dns.RcodeRefused)
	}
	r.mu.RLock()
	name := strings.TrimSuffix(strings.ToLower(question.Name), ".")
	known, found := r.records[name]
	macRecord, dynamic := r.macRecords[name]
	if dynamic {
		found = true
	}
	upstream := r.config.Upstream
	backupUpstream := r.config.BackupUpstream
	r.mu.RUnlock()
	mihomoQuery := r.proxy != nil && r.proxy.contains(name)
	if mihomoQuery {
		found = false
	}
	if found {
		out := reply(q, dns.RcodeSuccess)
		out.Authoritative = true
		if dynamic && (question.Qtype == dns.TypeA || question.Qtype == dns.TypeANY) {
			if r.ipForMAC == nil {
				r.failed.Add(1)
				return reply(q, dns.RcodeServerFailure)
			}
			ip, ok := r.ipForMAC(macRecord.MAC)
			if !ok {
				r.failed.Add(1)
				return reply(q, dns.RcodeServerFailure)
			}
			macRecord.Value = ip.String()
			out.Answer = append(out.Answer, recordRR(macRecord))
		}
		for _, rr := range known {
			if rr.Header().Rrtype == question.Qtype || question.Qtype == dns.TypeANY {
				out.Answer = append(out.Answer, dns.Copy(rr))
			}
		}
		r.local.Add(1)
		return out
	}
	select {
	case r.slots <- struct{}{}:
		defer func() { <-r.slots }()
	default:
		r.failed.Add(1)
		return reply(q, dns.RcodeServerFailure)
	}
	r.forwarded.Add(1)
	upstreams := make([]string, 0, 2)
	if mihomoQuery {
		upstreams = append(upstreams, r.mihomoDNSAddr)
		r.mihomo.Add(1)
	} else {
		for _, address := range []string{upstream, backupUpstream} {
			if address != "" && (len(upstreams) == 0 || upstreams[0] != net.JoinHostPort(address, "53")) {
				upstreams = append(upstreams, net.JoinHostPort(address, "53"))
			}
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 6*time.Second)
	defer cancel()
	exchange := r.exchange
	if exchange == nil {
		exchange = exchangeUpstream
	}
	for _, address := range upstreams {
		out := q.Copy()
		out.Id = dns.Id()
		out.Compress = true
		answer, err := exchange(ctx, out, transport, address)
		if err != nil || answer == nil || !answer.Response || answer.Opcode != q.Opcode || len(answer.Question) != 1 || !strings.EqualFold(answer.Question[0].Name, question.Name) || answer.Question[0].Qtype != question.Qtype || answer.Question[0].Qclass != question.Qclass {
			continue
		}
		if answer.Rcode == dns.RcodeServerFailure || answer.Rcode == dns.RcodeRefused || answer.Rcode == dns.RcodeNotImplemented {
			continue
		}
		answer.Id = q.Id
		answer.AuthenticatedData = false
		return answer
	}
	r.failed.Add(1)
	return reply(q, dns.RcodeServerFailure)
}

func exchangeUpstream(ctx context.Context, q *dns.Msg, transport, address string) (*dns.Msg, error) {
	client := &dns.Client{Net: transport, Timeout: 2500 * time.Millisecond, UDPSize: 1232}
	answer, _, err := client.ExchangeContext(ctx, q, address)
	if err == nil && answer != nil && answer.Truncated && transport == "udp" {
		client.Net = "tcp"
		answer, _, err = client.ExchangeContext(ctx, q, address)
	}
	return answer, err
}

func (r *Resolver) serveDNS(w dns.ResponseWriter, q *dns.Msg, allowed netip.Prefix) {
	host, _, err := net.SplitHostPort(w.RemoteAddr().String())
	ip, parseErr := netip.ParseAddr(host)
	if err != nil || parseErr != nil || !allowed.Contains(ip.Unmap()) {
		_ = w.WriteMsg(reply(q, dns.RcodeRefused))
		return
	}
	transport := "udp"
	if _, ok := w.RemoteAddr().(*net.TCPAddr); ok {
		transport = "tcp"
	}
	answer := r.resolve(context.Background(), q, transport)
	if r.proxy != nil && len(q.Question) == 1 {
		r.proxy.observeDNS(strings.TrimSuffix(strings.ToLower(q.Question[0].Name), "."), answer)
	}
	if transport == "udp" {
		size := 512
		if opt := q.IsEdns0(); opt != nil {
			size = int(opt.UDPSize())
			if size < 512 {
				size = 512
			}
			if size > 1232 {
				size = 1232
			}
		}
		answer.Truncate(size)
	}
	_ = w.WriteMsg(answer)
}
