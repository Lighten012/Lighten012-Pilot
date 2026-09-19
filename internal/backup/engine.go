package backup

import (
	"fmt"
	"github.com/Lighten012/Lighten012-Pilot/internal/firewall"
	"github.com/Lighten012/Lighten012-Pilot/internal/network"
	"github.com/Lighten012/Lighten012-Pilot/internal/services"
	"time"
)

type System struct {
	Network  *network.Manager
	Services *services.Manager
	Firewall *firewall.Manager
}

func (s *System) Snapshot() (Bundle, error) {
	n, err := s.Network.Status()
	if err != nil {
		return Bundle{}, err
	}
	if n.Saved == nil {
		return Bundle{}, fmt.Errorf("请先完成并确认 WAN/LAN 配置")
	}
	f, err := s.Firewall.State()
	if err != nil {
		return Bundle{}, err
	}
	if n.Pending != nil || f.Pending != nil {
		return Bundle{}, fmt.Errorf("请先确认或回滚已有网络/防火墙变更")
	}
	if s.Firewall.Enabled() && !f.Ready {
		return Bundle{}, fmt.Errorf("防火墙恢复尚未完成")
	}
	v, err := s.Services.State()
	if err != nil {
		return Bundle{}, err
	}
	return Bundle{Format: "Lighten012-Pilot", Version: 1, CreatedAt: time.Now().UnixMilli(), Network: *n.Saved, Services: v.Config, Firewall: f.Config}, nil
}
func (s *System) Validate(b Bundle) (Bundle, error) {
	if b.Format != "Lighten012-Pilot" || b.Version != 1 {
		return b, fmt.Errorf("不支持的备份格式或版本")
	}
	p, err := s.Network.Preview(b.Network)
	if err != nil {
		return b, err
	}
	b.Network = p.Config
	b.Services.DNS.Records = append([]services.Record{}, b.Services.DNS.Records...)
	b.Services, err = services.Normalize(b.Services)
	if err != nil {
		return b, err
	}
	b.Firewall, err = firewall.Normalize(b.Firewall)
	if err != nil {
		return b, err
	}
	n, err := s.Network.Status()
	if err != nil {
		return b, err
	}
	n.Saved = &b.Network
	// Validate dependencies against the prospective addresses without applying.
	n.Inventory.Devices = append([]network.Device{}, n.Inventory.Devices...)
	for i, d := range n.Inventory.Devices {
		for _, p := range []network.Port{b.Network.WAN, b.Network.LAN} {
			if d.Name == p.Interface && p.Mode == "static" {
				n.Inventory.Devices[i].Addresses = []string{p.Address}
			}
		}
	}
	if err = services.ValidateLAN(b.Services, n); err != nil {
		return b, err
	}
	b.Firewall, err = firewall.Bind(b.Firewall, n)
	if err != nil {
		return b, err
	}
	// Syntax/backend and occupied-host-port checks are repeated on actual apply.
	if err = (&firewall.Linux{}).Check(b.Firewall); err != nil {
		return b, err
	}
	return b, nil
}
func (s *System) setServices(c services.Config) error {
	p, err := s.Services.Preview(c)
	if err != nil {
		return err
	}
	return s.Services.Apply(services.ApplyRequest{Config: p.Config, Token: p.Token, Revision: p.Revision})
}
func (s *System) setFirewall(c firewall.Config) error {
	p, err := s.Firewall.Preview(c)
	if err != nil {
		return err
	}
	id, err := s.Firewall.Apply(p)
	if err != nil {
		return err
	}
	return s.Firewall.Confirm(id)
}
func (s *System) settleNetwork() error {
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		n, err := s.Network.Status()
		if err != nil {
			return err
		}
		if n.Pending == nil {
			return nil
		}
		_ = s.Network.Rollback(n.Pending.ID)
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("网络变更尚未结束，稍后重试恢复")
}
func (s *System) setNetwork(c network.Config) error {
	n, err := s.Network.Status()
	if err != nil {
		return err
	}
	if n.Saved != nil && same(*n.Saved, c) {
		return nil
	}
	p, err := s.Network.Preview(c)
	if err != nil {
		return err
	}
	id, err := s.Network.Apply(network.ApplyRequest{Config: p.Config, Token: p.Token, Revision: p.Revision})
	if err != nil {
		return err
	}
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		n, err = s.Network.Status()
		if err != nil {
			return err
		}
		if n.Pending == nil {
			return fmt.Errorf("网络应用未完成: %s", n.Event)
		}
		if n.Pending.ID != id {
			return fmt.Errorf("网络事务已改变")
		}
		if n.Pending.Phase == "awaiting_confirmation" {
			return s.Network.Confirm(id)
		}
		if n.Pending.Phase == "rollback_failed" {
			return fmt.Errorf("网络恢复失败: %s", n.Event)
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("网络应用超时")
}
func (s *System) Apply(b Bundle) error {
	// Clear incomplete child transactions after failures/restarts first. The outer
	// persisted journal retains the old full configuration until user confirmation.
	if err := s.settleNetwork(); err != nil {
		return err
	}
	f, err := s.Firewall.State()
	if err != nil {
		return err
	}
	if f.Pending != nil {
		if err = s.Firewall.Rollback(f.Pending.ID); err != nil {
			return err
		}
	}
	if err = s.setFirewall(firewall.Default()); err != nil {
		return fmt.Errorf("暂停防火墙: %w", err)
	}
	if err = s.setServices(services.Default()); err != nil {
		return fmt.Errorf("暂停 DHCP/DNS: %w", err)
	}
	if err = s.setNetwork(b.Network); err != nil {
		return err
	}
	if err = s.setServices(b.Services); err != nil {
		return fmt.Errorf("恢复 DHCP/DNS: %w", err)
	}
	if err = s.setFirewall(b.Firewall); err != nil {
		return fmt.Errorf("恢复防火墙: %w", err)
	}
	return nil
}
