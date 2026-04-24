//go:build linux

package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"syscall"

	"github.com/vishvananda/netlink"
)

var errSkipNeighborEvent = errors.New("skip non-IP neighbor event")

type NetlinkSource struct {
	logger      *slog.Logger
	recordError func(string)
}

func NewNetlinkSource(logger *slog.Logger, recordError func(string)) *NetlinkSource {
	return &NetlinkSource{logger: logger, recordError: recordError}
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
					if errors.Is(err, errSkipNeighborEvent) {
						s.logger.Debug("skipping non-IP neighbor update")
						continue
					}
					s.recordStageError("netlink_parse")
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
	s.recordStageError("netlink_receive")
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
	if u.Family != syscall.AF_INET && u.Family != syscall.AF_INET6 {
		return NeighborEvent{}, errSkipNeighborEvent
	}
	if u.IP == nil {
		return NeighborEvent{}, fmt.Errorf("nil IP in neighbor update")
	}
	addr, ok := netip.AddrFromSlice(u.IP)
	if !ok {
		return NeighborEvent{}, fmt.Errorf("invalid IP: %v", u.IP)
	}
	addr = addr.Unmap()

	return NeighborEvent{
		Type:         u.Type,
		LinkIndex:    u.LinkIndex,
		MasterIndex:  u.MasterIndex,
		Family:       u.Family,
		IP:           addr,
		HardwareAddr: u.HardwareAddr,
		State:        u.State,
	}, nil
}
