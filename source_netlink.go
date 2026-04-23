//go:build linux

package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/netip"
	"syscall"

	"github.com/vishvananda/netlink"
)

type NetlinkSource struct {
	logger *slog.Logger
}

func NewNetlinkSource(logger *slog.Logger) *NetlinkSource {
	return &NetlinkSource{logger: logger}
}

func (s *NetlinkSource) Subscribe(ctx context.Context) (<-chan NeighborEvent, error) {
	updates := make(chan netlink.NeighUpdate, 256)
	done := make(chan struct{})

	if err := netlink.NeighSubscribeWithOptions(updates, done, netlink.NeighSubscribeOptions{
		ListExisting:  true,
		ErrorCallback: s.errorCallback,
	}); err != nil {
		return nil, fmt.Errorf("netlink subscribe: %w", err)
	}

	out := make(chan NeighborEvent, 256)

	go func() {
		defer close(out)
		defer close(done)
		for {
			select {
			case <-ctx.Done():
				return
			case u, ok := <-updates:
				if !ok {
					return
				}
				ev, err := convertNeighUpdate(u)
				if err != nil {
					s.logger.Debug("skipping neighbor update", "error", err)
					continue
				}
				select {
				case out <- ev:
				case <-ctx.Done():
					return
				}
			}
		}
	}()

	return out, nil
}

func (s *NetlinkSource) errorCallback(err error) {
	s.logger.Error("netlink subscription error", "error", err)
}

type NetlinkResolver struct{}

func (r *NetlinkResolver) LinkName(index int) (string, error) {
	link, err := netlink.LinkByIndex(index)
	if err != nil {
		return "", err
	}
	return link.Attrs().Name, nil
}

func convertNeighUpdate(u netlink.NeighUpdate) (NeighborEvent, error) {
	if u.IP == nil {
		return NeighborEvent{}, fmt.Errorf("nil IP in neighbor update")
	}
	addr, ok := netip.AddrFromSlice(u.IP)
	if !ok {
		return NeighborEvent{}, fmt.Errorf("invalid IP: %v", u.IP)
	}
	addr = addr.Unmap()

	family := syscall.AF_INET
	if addr.Is6() {
		family = syscall.AF_INET6
	}

	return NeighborEvent{
		Type:         u.Type,
		LinkIndex:    u.LinkIndex,
		MasterIndex:  u.MasterIndex,
		Family:       family,
		IP:           addr,
		HardwareAddr: u.HardwareAddr,
		State:        u.State,
	}, nil
}
