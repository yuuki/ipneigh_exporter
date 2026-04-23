//go:build !linux

package main

import (
	"context"
	"fmt"
	"log/slog"
	"runtime"
)

type NetlinkSource struct {
	logger *slog.Logger
}

func NewNetlinkSource(logger *slog.Logger) *NetlinkSource {
	return &NetlinkSource{logger: logger}
}

func (s *NetlinkSource) Subscribe(_ context.Context) (<-chan NeighborEvent, error) {
	return nil, fmt.Errorf("netlink not supported on %s", runtime.GOOS)
}

type NetlinkResolver struct{}

func (r *NetlinkResolver) LinkName(_ int) (string, error) {
	return "", fmt.Errorf("link resolution not supported on %s", runtime.GOOS)
}
