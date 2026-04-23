package main

import (
	"context"
	"net"
	"net/netip"
)

type NeighborEvent struct {
	Type         uint16 // RTM_NEWNEIGH or RTM_DELNEIGH
	LinkIndex    int
	MasterIndex  int // VRF master, 0 if none
	Family       int
	IP           netip.Addr
	HardwareAddr net.HardwareAddr
	State        int // NUD_* flags
}

type NeighborSource interface {
	Subscribe(ctx context.Context) (<-chan NeighborEvent, error)
}
