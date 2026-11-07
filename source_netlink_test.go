//go:build linux

package main

import (
	"bytes"
	"context"
	"errors"
	"net"
	"net/netip"
	"reflect"
	"runtime"
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

// drainEvents collects what forward sends until it closes out, failing fast if
// it never does.
func drainEvents(t *testing.T, out <-chan NeighborEvent) []NeighborEvent {
	t.Helper()
	var got []NeighborEvent
	timeout := time.After(3 * time.Second)
	for {
		select {
		case ev, ok := <-out:
			if !ok {
				return got
			}
			got = append(got, ev)
		case <-timeout:
			t.Fatalf("timeout waiting for out to close, got %+v", got)
		}
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

	got := drainEvents(t, out)
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

// A consumer that sees out closed may rely on the subscription being over, so
// forward has to close done first. Closed the other way round, the two closes
// are only an instant apart: a receiver parked on out would be woken after
// both, so this one spins on out instead, over many trials. It yields now and
// then so that with a single P the forwarder still gets to run; that host
// cannot catch a reversed order, but it does not slow down either.
func TestNetlinkSourceForward_ClosesDoneBeforeOut(t *testing.T) {
	source, _ := newForwardTestSource()
	for trial := 0; trial < 5000; trial++ {
		updates := make(chan netlink.NeighUpdate)
		close(updates)
		done := make(chan struct{})
		out := make(chan NeighborEvent)
		go source.forward(t.Context(), updates, done, out)

		deadline := time.Now().Add(3 * time.Second)
		for spins := 0; ; spins++ {
			select {
			case _, ok := <-out:
				if ok {
					t.Fatalf("trial %d: unexpected event", trial)
				}
			default:
				if spins%1024 == 1023 {
					runtime.Gosched()
					if time.Now().After(deadline) {
						t.Fatalf("trial %d: timeout waiting for out to close", trial)
					}
				}
				continue
			}
			break
		}
		select {
		case <-done:
		default:
			t.Fatalf("trial %d: out was closed before done", trial)
		}
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

	// Once the update has been taken, the forwarder is converting it or
	// sending it; with nobody receiving, the send can only end through ctx.
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

func TestNetlinkSourceDeliver_ReportsFalseWhenContextEndsBeforeSend(t *testing.T) {
	source, _ := newForwardTestSource()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	update := netlink.NeighUpdate{Type: RTM_NEWNEIGH, Neigh: netlink.Neigh{
		LinkIndex: 2, Family: syscall.AF_INET, IP: net.ParseIP("192.0.2.1"),
	}}
	out := make(chan NeighborEvent) // nobody receives, so only ctx can end the send
	result := make(chan bool, 1)
	go func() { result <- source.deliver(ctx, update, out) }()

	select {
	case delivered := <-result:
		if delivered {
			t.Fatal("deliver reported true although ctx ended before the event was taken")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("deliver blocked although ctx had ended")
	}
}

// convertNeighUpdate is where kernel neighbor messages, already decoded by
// the netlink library, become events. Non-IP families are skipped, addresses
// that are not 4 or 16 bytes are parse errors, and everything else must come
// through unchanged except for the IP, which is unmapped from IPv4-in-IPv6.
func FuzzConvertNeigh(f *testing.F) {
	f.Add(uint16(RTM_NEWNEIGH), uint8(syscall.AF_INET), false, []byte{192, 0, 2, 1}, []byte{2, 0, 0, 0, 0, 1}, int32(2), int32(0), int32(netlink.NUD_REACHABLE))
	f.Add(uint16(RTM_DELNEIGH), uint8(syscall.AF_INET6), false, []byte(net.ParseIP("2001:db8::1")), []byte{}, int32(3), int32(7), int32(netlink.NUD_STALE))
	f.Add(uint16(RTM_NEWNEIGH), uint8(syscall.AF_INET), false, []byte(net.ParseIP("192.0.2.1")), []byte{}, int32(2), int32(0), int32(netlink.NUD_INCOMPLETE))
	f.Add(uint16(RTM_NEWNEIGH), uint8(syscall.AF_BRIDGE), true, []byte{}, []byte{2, 0, 0, 0, 0, 1}, int32(4), int32(5), int32(netlink.NUD_NOARP))
	f.Add(uint16(RTM_NEWNEIGH), uint8(syscall.AF_INET), true, []byte{}, []byte{2, 0, 0, 0, 0, 1}, int32(2), int32(0), int32(0))
	f.Add(uint16(RTM_NEWNEIGH), uint8(syscall.AF_INET6), false, []byte{0xfe, 0x80, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}, []byte{2, 0, 0, 0, 0, 1}, int32(2), int32(0), int32(0))

	f.Fuzz(func(t *testing.T, typ uint16, family uint8, nilIP bool, ipBytes, hw []byte, link, master, state int32) {
		n := netlink.Neigh{
			LinkIndex: int(link), MasterIndex: int(master), Family: int(family), State: int(state),
			IP: net.IP(ipBytes), HardwareAddr: net.HardwareAddr(hw),
		}
		if nilIP {
			n.IP = nil
		}
		ev, err := convertNeighUpdate(netlink.NeighUpdate{Type: typ, Neigh: n})

		if n.Family != syscall.AF_INET && n.Family != syscall.AF_INET6 {
			if !errors.Is(err, errSkipNeighborEvent) {
				t.Fatalf("family %d: err = %v, want errSkipNeighborEvent", n.Family, err)
			}
			return
		}
		if n.IP == nil || (len(n.IP) != net.IPv4len && len(n.IP) != net.IPv6len) {
			if err == nil || errors.Is(err, errSkipNeighborEvent) {
				t.Fatalf("IP %v: err = %v, want a parse error", []byte(n.IP), err)
			}
			return
		}
		if err != nil {
			t.Fatalf("convert %+v: %v", n, err)
		}
		if !ev.IP.IsValid() || ev.IP.Is4In6() {
			t.Fatalf("IP %v converted to %v, want a valid unmapped address", n.IP, ev.IP)
		}
		if !n.IP.Equal(net.IP(ev.IP.AsSlice())) {
			t.Fatalf("IP %v converted to %v", n.IP, ev.IP)
		}
		if ev.Type != typ || ev.LinkIndex != n.LinkIndex || ev.MasterIndex != n.MasterIndex ||
			ev.Family != n.Family || ev.State != n.State || !bytes.Equal(ev.HardwareAddr, hw) {
			t.Fatalf("convert %+v (type %d) = %+v", n, typ, ev)
		}
	})
}
