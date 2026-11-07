package main

import (
	"bytes"
	"net/netip"
	"strings"
	"syscall"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	dto "github.com/prometheus/client_model/go"
	"golang.org/x/time/rate"
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
# HELP ipneigh_entries Number of neighbor entries by state.
# TYPE ipneigh_entries gauge
ipneigh_entries{dev="eth0",family="ipv4",state="reachable",vrf=""} 2
ipneigh_entries{dev="eth0",family="ipv4",state="stale",vrf=""} 1
`
	if err := testutil.CollectAndCompare(collector, strings.NewReader(expected), "ipneigh_entries"); err != nil {
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
# HELP ipneigh_mac_change_total Number of MAC address changes detected for the same IP.
# TYPE ipneigh_mac_change_total counter
ipneigh_mac_change_total{dev="eth0",family="ipv4",ip="10.0.0.1",vrf=""} 1
`
	if err := testutil.CollectAndCompare(collector, strings.NewReader(expected), "ipneigh_mac_change_total"); err != nil {
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

	count := testutil.CollectAndCount(collector, "ipneigh_last_flap_unix_seconds")
	if count != 1 {
		t.Errorf("expected 1 last_flap metric, got %d", count)
	}
}

// collectAll gathers what c exposes, failing on any error the registry
// reports (including recovered collector panics).
func collectAll(t *testing.T, c prometheus.Collector) []*dto.MetricFamily {
	t.Helper()
	reg := prometheus.NewPedanticRegistry()
	if err := reg.Register(c); err != nil {
		t.Fatalf("register: %v", err)
	}
	families, err := reg.Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	return families
}

// Interface names come from the kernel, which accepts any bytes except NUL,
// '/', ':' and whitespace, so they need not be valid UTF-8. Whatever the
// names, IP and MACs, a scrape must succeed with valid label values, and
// valid names must be exposed unchanged.
func FuzzCollectorLabelValues(f *testing.F) {
	f.Add("eth0", "", []byte{10, 0, 0, 1}, []byte{0xaa, 0xbb, 0xcc, 0xdd, 0xee, 0x01}, []byte{0xaa, 0xbb, 0xcc, 0xdd, 0xee, 0x02}, NUD_REACHABLE)
	f.Add("veth-host", "vrf-red", netip.MustParseAddr("2001:db8::1").AsSlice(), []byte{0x02, 0, 0, 0, 0, 1}, []byte{}, NUD_STALE)
	f.Add("bond0.100", "mgmt", []byte{192, 0, 2, 1}, []byte{}, []byte{0x02, 0, 0, 0, 0, 1}, NUD_INCOMPLETE)
	f.Add("éth0", "vrf-ü", []byte{10, 0, 0, 2}, []byte{1, 2, 3, 4, 5, 6}, []byte{1, 2, 3, 4, 5, 6}, NUD_PERMANENT)
	// Regression: a flap on an interface whose name is not valid UTF-8 used to
	// panic in the watcher (CounterVec.WithLabelValues) and crash the exporter;
	// testdata/fuzz holds the scrape-time variant the fuzzer found.
	f.Add("v\xff\xfe", "vrf\xc3", []byte{192, 0, 2, 2}, []byte{2, 0, 0, 0, 0, 1}, []byte{2, 0, 0, 0, 0, 2}, NUD_REACHABLE)

	f.Fuzz(func(t *testing.T, devName, vrfName string, ipBytes, mac1, mac2 []byte, state int) {
		addr, ok := netip.AddrFromSlice(ipBytes)
		if !ok {
			return
		}
		family := syscall.AF_INET6
		if addr.Unmap().Is4() {
			family = syscall.AF_INET
		}
		store := NewNeighborStore(StoreConfig{
			SyncInterval: 15 * time.Minute,
			DeleteGrace:  30 * time.Second,
			FlapRate:     rate.Inf,
		}, &mockResolver{names: map[int]string{1: devName, 10: vrfName}}, testLogger())

		for _, hw := range [][]byte{mac1, mac2} {
			store.HandleEvent(NeighborEvent{
				Type: RTM_NEWNEIGH, LinkIndex: 1, MasterIndex: 10, Family: family,
				IP: addr, HardwareAddr: hw, State: state,
			})
		}

		wantFlap := len(mac1) > 0 && len(mac2) > 0 && !bytes.Equal(mac1, mac2)
		var entries, flaps float64
		for _, mf := range collectAll(t, NewNeighborCollector(store)) {
			for _, m := range mf.GetMetric() {
				for _, lp := range m.GetLabel() {
					if !utf8.ValidString(lp.GetValue()) {
						t.Fatalf("%s: label %s=%q is not valid UTF-8", mf.GetName(), lp.GetName(), lp.GetValue())
					}
					if lp.GetName() == "dev" && utf8.ValidString(devName) && lp.GetValue() != devName {
						t.Fatalf("%s: valid device name %q exposed as %q", mf.GetName(), devName, lp.GetValue())
					}
					if lp.GetName() == "vrf" && utf8.ValidString(vrfName) && lp.GetValue() != vrfName {
						t.Fatalf("%s: valid VRF name %q exposed as %q", mf.GetName(), vrfName, lp.GetValue())
					}
				}
				switch mf.GetName() {
				case "ipneigh_entries":
					entries += m.GetGauge().GetValue()
				case "ipneigh_mac_change_total":
					flaps += m.GetCounter().GetValue()
				}
			}
		}

		wantEntries, wantFlaps := 0.0, 0.0
		if devName != "" { // an empty name means the link did not resolve
			wantEntries = 1
			if wantFlap {
				wantFlaps = 1
			}
		}
		if entries != wantEntries || flaps != wantFlaps {
			t.Fatalf("entries=%v flaps=%v, want %v and %v", entries, flaps, wantEntries, wantFlaps)
		}
	})
}
