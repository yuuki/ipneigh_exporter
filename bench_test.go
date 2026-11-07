package main

import (
	"net"
	"net/netip"
	"syscall"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"golang.org/x/time/rate"
)

// Benchmarks of the per-event path (HandleEvent), the periodic resync and the
// scrape path (Collect). Run with `make bench` and compare with benchstat.

func benchStore(b *testing.B) *NeighborStore {
	b.Helper()
	return NewNeighborStore(StoreConfig{
		SyncInterval: 15 * time.Minute,
		DeleteGrace:  30 * time.Second,
		FlapRate:     rate.Inf,
	}, &mockResolver{names: map[int]string{1: "eth0", 10: "vrf-red"}}, testLogger())
}

// benchEvents returns n events for distinct IPv4 neighbors on eth0.
func benchEvents(n int) []NeighborEvent {
	events := make([]NeighborEvent, n)
	for i := range events {
		events[i] = NeighborEvent{
			Type: RTM_NEWNEIGH, LinkIndex: 1, MasterIndex: 10, Family: syscall.AF_INET,
			IP:           netip.AddrFrom4([4]byte{10, byte(i >> 16), byte(i >> 8), byte(i)}),
			HardwareAddr: net.HardwareAddr{2, 0, 0, byte(i >> 16), byte(i >> 8), byte(i)},
			State:        NUD_REACHABLE,
		}
	}
	return events
}

func BenchmarkHandleEvent_Refresh(b *testing.B) {
	s := benchStore(b)
	ev := benchEvents(1)[0]
	s.HandleEvent(ev)
	b.ReportAllocs()
	for b.Loop() {
		s.HandleEvent(ev)
	}
}

func BenchmarkHandleEvent_Flap(b *testing.B) {
	s := benchStore(b)
	evs := [2]NeighborEvent{benchEvents(1)[0], benchEvents(1)[0]}
	evs[1].HardwareAddr = net.HardwareAddr{2, 0, 0, 0, 0, 0xff}
	s.HandleEvent(evs[0])
	b.ReportAllocs()
	i := 0
	for b.Loop() {
		i++
		s.HandleEvent(evs[i%2])
	}
}

func BenchmarkSyncNeighbors_1k(b *testing.B) {
	s := benchStore(b)
	events := benchEvents(1000)
	b.ReportAllocs()
	for b.Loop() {
		s.SyncNeighbors(events)
	}
}

func BenchmarkCollect_1k(b *testing.B) {
	s := benchStore(b)
	for _, ev := range benchEvents(1000) {
		s.HandleEvent(ev)
	}
	c := NewNeighborCollector(s)
	ch := make(chan prometheus.Metric, 64)
	done := make(chan struct{})
	go func() {
		for range ch {
		}
		close(done)
	}()
	b.ReportAllocs()
	for b.Loop() {
		c.Collect(ch)
	}
	close(ch)
	<-done
}
