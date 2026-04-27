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
		ListExisting: true,
		ErrorCallback: func(err error) {
			s.handleSubscriptionError(ctx, err)
		},
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

func (s *NetlinkSource) List(ctx context.Context) ([]NeighborEvent, error) {
	var out []NeighborEvent
	for _, family := range []int{syscall.AF_INET, syscall.AF_INET6} {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		neighbors, err := netlink.NeighList(0, family)
		if err != nil {
			return nil, fmt.Errorf("netlink neigh list: %w", err)
		}
		for _, neigh := range neighbors {
			ev, err := convertNeigh(neigh)
			if err != nil {
				if errors.Is(err, errSkipNeighborEvent) {
					s.logger.Debug("skipping non-IP neighbor entry during sync")
					continue
				}
				s.recordStageError("netlink_parse")
				s.logger.Debug("skipping neighbor entry during sync", "error", err)
				continue
			}
			out = append(out, ev)
		}
	}
	return out, nil
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
	ev, err := convertNeigh(u.Neigh)
	if err != nil {
		return NeighborEvent{}, err
	}
	ev.Type = u.Type
	return ev, nil
}

func convertNeigh(n netlink.Neigh) (NeighborEvent, error) {
	if n.Family != syscall.AF_INET && n.Family != syscall.AF_INET6 {
		return NeighborEvent{}, errSkipNeighborEvent
	}
	if n.IP == nil {
		return NeighborEvent{}, fmt.Errorf("nil IP in neighbor update")
	}
	addr, ok := netip.AddrFromSlice(n.IP)
	if !ok {
		return NeighborEvent{}, fmt.Errorf("invalid IP: %v", n.IP)
	}
	addr = addr.Unmap()

	return NeighborEvent{
		Type:         RTM_NEWNEIGH,
		LinkIndex:    n.LinkIndex,
		MasterIndex:  n.MasterIndex,
		Family:       n.Family,
		IP:           addr,
		HardwareAddr: n.HardwareAddr,
		State:        n.State,
	}, nil
}
