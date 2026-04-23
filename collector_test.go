package main

import (
	"net/netip"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestCollector_Entries(t *testing.T) {
	store := testStore(t)

	store.HandleEvent(NeighborEvent{
		Type: RTM_NEWNEIGH, LinkIndex: 1, Family: syscall.AF_INET,
		IP: ip("10.0.0.1"), HardwareAddr: mac("aa:bb:cc:dd:ee:01"), State: NUD_REACHABLE,
	})
	store.HandleEvent(NeighborEvent{
		Type: RTM_NEWNEIGH, LinkIndex: 1, Family: syscall.AF_INET,
		IP: ip("10.0.0.2"), HardwareAddr: mac("aa:bb:cc:dd:ee:02"), State: NUD_REACHABLE,
	})
	store.HandleEvent(NeighborEvent{
		Type: RTM_NEWNEIGH, LinkIndex: 1, Family: syscall.AF_INET,
		IP: ip("10.0.0.3"), HardwareAddr: mac("aa:bb:cc:dd:ee:03"), State: NUD_STALE,
	})

	collector := NewNeighborCollector(store)
	reg := prometheus.NewRegistry()
	reg.MustRegister(collector)

	expected := `
# HELP linux_neighbor_entries Number of neighbor entries by state.
# TYPE linux_neighbor_entries gauge
linux_neighbor_entries{dev="eth0",family="ipv4",state="reachable",vrf=""} 2
linux_neighbor_entries{dev="eth0",family="ipv4",state="stale",vrf=""} 1
`
	if err := testutil.CollectAndCompare(collector, strings.NewReader(expected), "linux_neighbor_entries"); err != nil {
		t.Error(err)
	}
}

func TestCollector_FlapCounter(t *testing.T) {
	store := testStore(t)

	store.HandleEvent(NeighborEvent{
		Type: RTM_NEWNEIGH, LinkIndex: 1, Family: syscall.AF_INET,
		IP: ip("10.0.0.1"), HardwareAddr: mac("aa:bb:cc:dd:ee:01"), State: NUD_REACHABLE,
	})
	store.HandleEvent(NeighborEvent{
		Type: RTM_NEWNEIGH, LinkIndex: 1, Family: syscall.AF_INET,
		IP: ip("10.0.0.1"), HardwareAddr: mac("aa:bb:cc:dd:ee:02"), State: NUD_REACHABLE,
	})

	collector := NewNeighborCollector(store)

	expected := `
# HELP linux_neighbor_mac_change_total Number of MAC address changes detected for the same IP.
# TYPE linux_neighbor_mac_change_total counter
linux_neighbor_mac_change_total{dev="eth0",family="ipv4",ip="10.0.0.1",vrf=""} 1
`
	if err := testutil.CollectAndCompare(collector, strings.NewReader(expected), "linux_neighbor_mac_change_total"); err != nil {
		t.Error(err)
	}
}

func TestCollector_LastFlap(t *testing.T) {
	store := testStore(t)

	flapTime := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return flapTime }

	store.HandleEvent(NeighborEvent{
		Type: RTM_NEWNEIGH, LinkIndex: 1, Family: syscall.AF_INET,
		IP: netip.MustParseAddr("10.0.0.1"), HardwareAddr: mac("aa:bb:cc:dd:ee:01"), State: NUD_REACHABLE,
	})
	store.HandleEvent(NeighborEvent{
		Type: RTM_NEWNEIGH, LinkIndex: 1, Family: syscall.AF_INET,
		IP: netip.MustParseAddr("10.0.0.1"), HardwareAddr: mac("aa:bb:cc:dd:ee:02"), State: NUD_REACHABLE,
	})

	collector := NewNeighborCollector(store)

	count := testutil.CollectAndCount(collector, "linux_neighbor_last_flap_unix_seconds")
	if count != 1 {
		t.Errorf("expected 1 last_flap metric, got %d", count)
	}
}
