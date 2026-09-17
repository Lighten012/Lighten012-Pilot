package services

import (
	"context"
	"net"
	"net/netip"
	"strings"
	"sync/atomic"
	"time"

	"github.com/miekg/dns"
)

type Counters struct {
	Queries   uint64 `json:"queries"`
	Local     uint64 `json:"local"`
	Forwarded uint64 `json:"forwarded"`
	Failed    uint64 `json:"failed"`
}
type Resolver struct {
	records                           map[string][]dns.RR
	upstream                          string
	allowed                           netip.Prefix
	slots                             chan struct{}
	queries, local, forwarded, failed atomic.Uint64
}

func NewResolver(c Config) *Resolver {
	p, _ := netip.ParsePrefix(c.Address)
	r := &Resolver{records: map[string][]dns.RR{}, upstream: net.JoinHostPort(c.DNS.Upstream, "53"), allowed: p, slots: make(chan struct{}, 128)}
	for _, v := range c.DNS.Records {
		h := dns.RR_Header{Name: v.Name, Class: dns.ClassINET, Ttl: v.TTL}
		var rr dns.RR
		if v.Type == "A" {
			h.Rrtype = dns.TypeA
			rr = &dns.A{Hdr: h, A: net.ParseIP(v.Value).To4()}
		} else {
			h.Rrtype = dns.TypeAAAA
			rr = &dns.AAAA{Hdr: h, AAAA: net.ParseIP(v.Value)}
		}
		r.records[v.Name] = append(r.records[v.Name], rr)
	}
	return r
}
func (r *Resolver) Stats() Counters {
	return Counters{r.queries.Load(), r.local.Load(), r.forwarded.Load(), r.failed.Load()}
}
func errorReply(q *dns.Msg, code int) *dns.Msg {
	m := new(dns.Msg)
	m.SetRcode(q, code)
	m.RecursionAvailable = true
	return m
}
func (r *Resolver) Resolve(ctx context.Context, q *dns.Msg) *dns.Msg {
	r.queries.Add(1)
	if q.Response || q.Opcode != dns.OpcodeQuery || len(q.Question) != 1 || q.Question[0].Qclass != dns.ClassINET {
		return errorReply(q, dns.RcodeFormatError)
	}
	question := q.Question[0]
	if question.Qtype == dns.TypeAXFR || question.Qtype == dns.TypeIXFR || q.IsTsig() != nil {
		return errorReply(q, dns.RcodeRefused)
	}
	if records, ok := r.records[strings.ToLower(question.Name)]; ok {
		r.local.Add(1)
		m := errorReply(q, dns.RcodeSuccess)
		m.Authoritative = true
		m.AuthenticatedData = false
		for _, rr := range records {
			if rr.Header().Rrtype == question.Qtype || question.Qtype == dns.TypeANY {
				m.Answer = append(m.Answer, dns.Copy(rr))
			}
		}
		// A locally owned name never falls through on AAAA/HTTPS/other types.
		// NODATA prevents clients from resolving a public address for the same name.
		return m
	}
	select {
	case r.slots <- struct{}{}:
		defer func() { <-r.slots }()
	default:
		r.failed.Add(1)
		return errorReply(q, dns.RcodeServerFailure)
	}
	r.forwarded.Add(1)
	ctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	out := q.Copy()
	out.Id = dns.Id()
	out.Compress = true
	client := &dns.Client{Net: "udp", Timeout: 2 * time.Second, UDPSize: 1232}
	answer, _, e := client.ExchangeContext(ctx, out, r.upstream)
	if e == nil && answer.Truncated {
		client.Net = "tcp"
		answer, _, e = client.ExchangeContext(ctx, out, r.upstream)
	}
	if e != nil || answer == nil || !answer.Response || answer.Opcode != q.Opcode || len(answer.Question) != 1 || !strings.EqualFold(answer.Question[0].Name, question.Name) || answer.Question[0].Qtype != question.Qtype || answer.Question[0].Qclass != question.Qclass {
		r.failed.Add(1)
		return errorReply(q, dns.RcodeServerFailure)
	}
	answer.Id = q.Id
	answer.AuthenticatedData = false
	answer.Compress = true
	return answer
}
func (r *Resolver) ServeDNS(w dns.ResponseWriter, q *dns.Msg) {
	host, _, e := net.SplitHostPort(w.RemoteAddr().String())
	ip, e2 := netip.ParseAddr(host)
	if e != nil || e2 != nil || !r.allowed.Contains(ip.Unmap()) {
		_ = w.WriteMsg(errorReply(q, dns.RcodeRefused))
		return
	}
	answer := r.Resolve(context.Background(), q)
	if _, ok := w.RemoteAddr().(*net.UDPAddr); ok {
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
