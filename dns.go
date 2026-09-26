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
	Failed    uint64 `json:"failed"`
}

type Resolver struct {
	mu                                sync.RWMutex
	config                            Config
	records                           map[string][]dns.RR
	slots                             chan struct{}
	queries, local, forwarded, failed atomic.Uint64
}

func newResolver(c Config) *Resolver {
	r := &Resolver{slots: make(chan struct{}, 128)}
	r.setConfig(c)
	return r
}

func (r *Resolver) setConfig(c Config) {
	records := make(map[string][]dns.RR)
	for _, v := range c.Records {
		records[v.Name] = append(records[v.Name], recordRR(v))
	}
	r.mu.Lock()
	r.config = c
	r.records = records
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
	return Stats{r.queries.Load(), r.local.Load(), r.forwarded.Load(), r.failed.Load()}
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
	known, found := r.records[strings.TrimSuffix(strings.ToLower(question.Name), ".")]
	upstream := r.config.Upstream
	if found {
		out := reply(q, dns.RcodeSuccess)
		out.Authoritative = true
		for _, rr := range known {
			if rr.Header().Rrtype == question.Qtype || question.Qtype == dns.TypeANY {
				out.Answer = append(out.Answer, dns.Copy(rr))
			}
		}
		r.mu.RUnlock()
		r.local.Add(1)
		return out
	}
	r.mu.RUnlock()
	select {
	case r.slots <- struct{}{}:
		defer func() { <-r.slots }()
	default:
		r.failed.Add(1)
		return reply(q, dns.RcodeServerFailure)
	}
	r.forwarded.Add(1)
	ctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	out := q.Copy()
	out.Id = dns.Id()
	out.Compress = true
	client := &dns.Client{Net: transport, Timeout: 3 * time.Second, UDPSize: 1232}
	answer, _, err := client.ExchangeContext(ctx, out, net.JoinHostPort(upstream, "53"))
	if err == nil && answer.Truncated && transport == "udp" {
		client.Net = "tcp"
		answer, _, err = client.ExchangeContext(ctx, out, net.JoinHostPort(upstream, "53"))
	}
	if err != nil || answer == nil || !answer.Response || answer.Opcode != q.Opcode || len(answer.Question) != 1 || !strings.EqualFold(answer.Question[0].Name, question.Name) || answer.Question[0].Qtype != question.Qtype || answer.Question[0].Qclass != question.Qclass {
		r.failed.Add(1)
		return reply(q, dns.RcodeServerFailure)
	}
	answer.Id = q.Id
	answer.AuthenticatedData = false
	return answer
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
