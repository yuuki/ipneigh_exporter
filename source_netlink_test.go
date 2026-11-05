//go:build linux

package main

import (
	"context"
	"net"
	"net/netip"
	"reflect"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/vishvananda/netlink"
)

func newForwardTestSource() (*NetlinkSource, func() []string) {
	var (
		mu     sync.Mutex
		stages []string
	)
	source := NewNetlinkSource(testLogger(), func(stage string) {
		mu.Lock()
		defer mu.Unlock()
		stages = append(stages, stage)
	})
	return source, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), stages...)
	}
}

func waitClosed[T any](t *testing.T, name string, ch <-chan T) {
	t.Helper()
	select {
	case _, ok := <-ch:
		if ok {
			t.Fatalf("expected %s to be closed, received a value", name)
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("timeout waiting for %s to close", name)
	}
}

func TestNetlinkSourceForward_RelaysConvertibleUpdatesInOrder(t *testing.T) {
	source, recorded := newForwardTestSource()
	mac := net.HardwareAddr{0x02, 0, 0, 0, 0, 0x01}

	updates := make(chan netlink.NeighUpdate, 4)
	updates <- netlink.NeighUpdate{Type: RTM_NEWNEIGH, Neigh: netlink.Neigh{
		LinkIndex: 2, Family: syscall.AF_INET, State: netlink.NUD_REACHABLE,
		IP: net.ParseIP("192.0.2.1"), HardwareAddr: mac,
	}}
	updates <- netlink.NeighUpdate{Type: RTM_NEWNEIGH, Neigh: netlink.Neigh{
		LinkIndex: 2, Family: syscall.AF_BRIDGE, HardwareAddr: mac,
	}}
	updates <- netlink.NeighUpdate{Type: RTM_NEWNEIGH, Neigh: netlink.Neigh{
		LinkIndex: 2, Family: syscall.AF_INET,
	}}
	updates <- netlink.NeighUpdate{Type: RTM_DELNEIGH, Neigh: netlink.Neigh{
		LinkIndex: 3, MasterIndex: 7, Family: syscall.AF_INET6, State: netlink.NUD_STALE,
		IP: net.ParseIP("2001:db8::1"),
	}}
	close(updates)

	done := make(chan struct{})
	out := make(chan NeighborEvent, 4)
	go source.forward(t.Context(), updates, done, out)

	var got []NeighborEvent
	for ev := range out {
		got = append(got, ev)
	}
	want := []NeighborEvent{
		{Type: RTM_NEWNEIGH, LinkIndex: 2, Family: syscall.AF_INET, IP: netip.MustParseAddr("192.0.2.1"),
			HardwareAddr: mac, State: netlink.NUD_REACHABLE},
		{Type: RTM_DELNEIGH, LinkIndex: 3, MasterIndex: 7, Family: syscall.AF_INET6,
			IP: netip.MustParseAddr("2001:db8::1"), State: netlink.NUD_STALE},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("events = %+v, want %+v", got, want)
	}
	waitClosed(t, "done", done)
	// The non-IP update is skipped silently; only the unparsable one counts.
	if stages := recorded(); !reflect.DeepEqual(stages, []string{"netlink_parse"}) {
		t.Fatalf("recorded stages = %v, want [netlink_parse]", stages)
	}
}

func TestNetlinkSourceForward_StopsWhenContextEndsWhileWaiting(t *testing.T) {
	source, recorded := newForwardTestSource()
	ctx, cancel := context.WithCancel(t.Context())

	updates := make(chan netlink.NeighUpdate)
	done := make(chan struct{})
	out := make(chan NeighborEvent)
	go source.forward(ctx, updates, done, out)

	cancel()
	waitClosed(t, "out", out)
	waitClosed(t, "done", done)
	if stages := recorded(); len(stages) != 0 {
		t.Fatalf("expected no recorded errors, got %v", stages)
	}
}

func TestNetlinkSourceForward_StopsWhenContextEndsWhileSending(t *testing.T) {
	source, _ := newForwardTestSource()
	ctx, cancel := context.WithCancel(t.Context())

	updates := make(chan netlink.NeighUpdate, 1)
	updates <- netlink.NeighUpdate{Type: RTM_NEWNEIGH, Neigh: netlink.Neigh{
		LinkIndex: 2, Family: syscall.AF_INET, IP: net.ParseIP("192.0.2.1"),
	}}
	done := make(chan struct{})
	out := make(chan NeighborEvent) // nobody receives, so the send blocks
	go source.forward(ctx, updates, done, out)

	// Once the update is taken the forwarder is blocked on out.
	deadline := time.Now().Add(3 * time.Second)
	for len(updates) > 0 {
		if time.Now().After(deadline) {
			t.Fatal("forwarder never took the update")
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	waitClosed(t, "done", done)
	waitClosed(t, "out", out)
}
