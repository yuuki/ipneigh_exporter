package main

import (
	"log/slog"
	"net"
	"net/netip"
	"os"
	"syscall"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	io_prometheus_client "github.com/prometheus/client_model/go"
	"golang.org/x/time/rate"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

func testStore(t *testing.T) *NeighborStore {
	t.Helper()
	resolver := &mockResolver{names: map[int]string{1: "eth0", 2: "eth1", 10: "vrf-red"}}
	return NewNeighborStore(StoreConfig{
		StaleTTL:    15 * time.Minute,
		DeleteGrace: 30 * time.Second,
		FlapRate:    rate.Limit(10),
		FlapBurst:   5,
	}, resolver, testLogger())
}

func mac(s string) net.HardwareAddr {
	m, _ := net.ParseMAC(s)
	return m
}

func ip(s string) netip.Addr {
	return netip.MustParseAddr(s)
}

func counterValue(cv *prometheus.CounterVec, labels ...string) float64 {
	m := &io_prometheus_client.Metric{}
	c, err := cv.GetMetricWithLabelValues(labels...)
	if err != nil {
		return 0
	}
	c.Write(m)
	return m.GetCounter().GetValue()
}

func TestHandleEvent_FirstEntry(t *testing.T) {
	s := testStore(t)
	s.HandleEvent(NeighborEvent{
		Type: RTM_NEWNEIGH, LinkIndex: 1, Family: syscall.AF_INET,
		IP: ip("10.0.0.1"), HardwareAddr: mac("aa:bb:cc:dd:ee:01"), State: NUD_REACHABLE,
	})

	snap := s.Snapshot()
	if len(snap) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(snap))
	}
	key := NeighborKey{Dev: 1, IP: ip("10.0.0.1"), Family: syscall.AF_INET}
	entry := snap[key]
	if entry == nil {
		t.Fatal("entry not found")
	}
	if entry.MAC.String() != "aa:bb:cc:dd:ee:01" {
		t.Errorf("expected MAC aa:bb:cc:dd:ee:01, got %s", entry.MAC)
	}
	if entry.FlapCount != 0 {
		t.Errorf("expected 0 flaps, got %d", entry.FlapCount)
	}
}

func TestHandleEvent_SameMACSameState(t *testing.T) {
	s := testStore(t)
	ev := NeighborEvent{
		Type: RTM_NEWNEIGH, LinkIndex: 1, Family: syscall.AF_INET,
		IP: ip("10.0.0.1"), HardwareAddr: mac("aa:bb:cc:dd:ee:01"), State: NUD_REACHABLE,
	}
	s.HandleEvent(ev)
	ev.State = NUD_STALE
	s.HandleEvent(ev)

	snap := s.Snapshot()
	key := NeighborKey{Dev: 1, IP: ip("10.0.0.1"), Family: syscall.AF_INET}
	if snap[key].FlapCount != 0 {
		t.Error("state-only change should not count as flap")
	}
	if snap[key].State != NUD_STALE {
		t.Error("state should be updated")
	}
}

func TestHandleEvent_DifferentMAC_Flap(t *testing.T) {
	s := testStore(t)
	s.HandleEvent(NeighborEvent{
		Type: RTM_NEWNEIGH, LinkIndex: 1, Family: syscall.AF_INET,
		IP: ip("10.0.0.1"), HardwareAddr: mac("aa:bb:cc:dd:ee:01"), State: NUD_REACHABLE,
	})
	s.HandleEvent(NeighborEvent{
		Type: RTM_NEWNEIGH, LinkIndex: 1, Family: syscall.AF_INET,
		IP: ip("10.0.0.1"), HardwareAddr: mac("aa:bb:cc:dd:ee:02"), State: NUD_REACHABLE,
	})

	snap := s.Snapshot()
	key := NeighborKey{Dev: 1, IP: ip("10.0.0.1"), Family: syscall.AF_INET}
	if snap[key].FlapCount != 1 {
		t.Errorf("expected 1 flap, got %d", snap[key].FlapCount)
	}
	if snap[key].MAC.String() != "aa:bb:cc:dd:ee:02" {
		t.Error("MAC should be updated to new value")
	}

	val := counterValue(s.flapCounter, "eth0", "", "10.0.0.1", "ipv4")
	if val != 1 {
		t.Errorf("expected counter=1, got %f", val)
	}
}

func TestHandleEvent_IncompleteToReachable_NoFlap(t *testing.T) {
	s := testStore(t)
	s.HandleEvent(NeighborEvent{
		Type: RTM_NEWNEIGH, LinkIndex: 1, Family: syscall.AF_INET,
		IP: ip("10.0.0.1"), HardwareAddr: nil, State: NUD_INCOMPLETE,
	})
	s.HandleEvent(NeighborEvent{
		Type: RTM_NEWNEIGH, LinkIndex: 1, Family: syscall.AF_INET,
		IP: ip("10.0.0.1"), HardwareAddr: mac("aa:bb:cc:dd:ee:01"), State: NUD_REACHABLE,
	})

	snap := s.Snapshot()
	key := NeighborKey{Dev: 1, IP: ip("10.0.0.1"), Family: syscall.AF_INET}
	if snap[key].FlapCount != 0 {
		t.Error("INCOMPLETE->REACHABLE should not be a flap")
	}
}

func TestHandleEvent_DeleteAndReaddSameMAC_NoFlap(t *testing.T) {
	s := testStore(t)
	s.HandleEvent(NeighborEvent{
		Type: RTM_NEWNEIGH, LinkIndex: 1, Family: syscall.AF_INET,
		IP: ip("10.0.0.1"), HardwareAddr: mac("aa:bb:cc:dd:ee:01"), State: NUD_REACHABLE,
	})
	s.HandleEvent(NeighborEvent{
		Type: RTM_DELNEIGH, LinkIndex: 1, Family: syscall.AF_INET,
		IP: ip("10.0.0.1"), State: 0,
	})
	s.HandleEvent(NeighborEvent{
		Type: RTM_NEWNEIGH, LinkIndex: 1, Family: syscall.AF_INET,
		IP: ip("10.0.0.1"), HardwareAddr: mac("aa:bb:cc:dd:ee:01"), State: NUD_REACHABLE,
	})

	snap := s.Snapshot()
	key := NeighborKey{Dev: 1, IP: ip("10.0.0.1"), Family: syscall.AF_INET}
	if snap[key].FlapCount != 0 {
		t.Error("re-add with same MAC within grace should not be a flap")
	}
}

func TestHandleEvent_DeleteAndReaddDifferentMAC_Flap(t *testing.T) {
	s := testStore(t)
	s.HandleEvent(NeighborEvent{
		Type: RTM_NEWNEIGH, LinkIndex: 1, Family: syscall.AF_INET,
		IP: ip("10.0.0.1"), HardwareAddr: mac("aa:bb:cc:dd:ee:01"), State: NUD_REACHABLE,
	})
	s.HandleEvent(NeighborEvent{
		Type: RTM_DELNEIGH, LinkIndex: 1, Family: syscall.AF_INET,
		IP: ip("10.0.0.1"), State: 0,
	})
	s.HandleEvent(NeighborEvent{
		Type: RTM_NEWNEIGH, LinkIndex: 1, Family: syscall.AF_INET,
		IP: ip("10.0.0.1"), HardwareAddr: mac("aa:bb:cc:dd:ee:02"), State: NUD_REACHABLE,
	})

	snap := s.Snapshot()
	key := NeighborKey{Dev: 1, IP: ip("10.0.0.1"), Family: syscall.AF_INET}
	if snap[key].FlapCount != 1 {
		t.Errorf("expected 1 flap, got %d", snap[key].FlapCount)
	}
}

func TestHandleEvent_DeleteIncompleteReaddDifferentMAC_Flap(t *testing.T) {
	s := testStore(t)
	s.HandleEvent(NeighborEvent{
		Type: RTM_NEWNEIGH, LinkIndex: 1, Family: syscall.AF_INET,
		IP: ip("10.0.0.1"), HardwareAddr: mac("aa:bb:cc:dd:ee:01"), State: NUD_REACHABLE,
	})
	s.HandleEvent(NeighborEvent{
		Type: RTM_DELNEIGH, LinkIndex: 1, Family: syscall.AF_INET,
		IP: ip("10.0.0.1"), State: 0,
	})
	// Kernel auto-resolves with INCOMPLETE (no MAC) after delete
	s.HandleEvent(NeighborEvent{
		Type: RTM_NEWNEIGH, LinkIndex: 1, Family: syscall.AF_INET,
		IP: ip("10.0.0.1"), HardwareAddr: nil, State: NUD_INCOMPLETE,
	})
	s.HandleEvent(NeighborEvent{
		Type: RTM_NEWNEIGH, LinkIndex: 1, Family: syscall.AF_INET,
		IP: ip("10.0.0.1"), HardwareAddr: mac("aa:bb:cc:dd:ee:02"), State: NUD_REACHABLE,
	})

	snap := s.Snapshot()
	key := NeighborKey{Dev: 1, IP: ip("10.0.0.1"), Family: syscall.AF_INET}
	if snap[key].FlapCount != 1 {
		t.Errorf("expected 1 flap despite INCOMPLETE in between, got %d", snap[key].FlapCount)
	}
}

func TestHandleEvent_DeleteGraceExpired_NoFlap(t *testing.T) {
	s := testStore(t)
	baseTime := time.Now()
	s.now = func() time.Time { return baseTime }

	s.HandleEvent(NeighborEvent{
		Type: RTM_NEWNEIGH, LinkIndex: 1, Family: syscall.AF_INET,
		IP: ip("10.0.0.1"), HardwareAddr: mac("aa:bb:cc:dd:ee:01"), State: NUD_REACHABLE,
	})

	s.now = func() time.Time { return baseTime.Add(1 * time.Second) }
	s.HandleEvent(NeighborEvent{
		Type: RTM_DELNEIGH, LinkIndex: 1, Family: syscall.AF_INET,
		IP: ip("10.0.0.1"), State: 0,
	})

	s.now = func() time.Time { return baseTime.Add(1*time.Minute + 1*time.Second) }
	s.HandleEvent(NeighborEvent{
		Type: RTM_NEWNEIGH, LinkIndex: 1, Family: syscall.AF_INET,
		IP: ip("10.0.0.1"), HardwareAddr: mac("aa:bb:cc:dd:ee:02"), State: NUD_REACHABLE,
	})

	snap := s.Snapshot()
	key := NeighborKey{Dev: 1, IP: ip("10.0.0.1"), Family: syscall.AF_INET}
	if snap[key].FlapCount != 0 {
		t.Error("after grace expired, different MAC should not be a flap")
	}
}

func TestHandleEvent_DifferentDevices_Independent(t *testing.T) {
	s := testStore(t)
	s.HandleEvent(NeighborEvent{
		Type: RTM_NEWNEIGH, LinkIndex: 1, Family: syscall.AF_INET,
		IP: ip("10.0.0.1"), HardwareAddr: mac("aa:bb:cc:dd:ee:01"), State: NUD_REACHABLE,
	})
	s.HandleEvent(NeighborEvent{
		Type: RTM_NEWNEIGH, LinkIndex: 2, Family: syscall.AF_INET,
		IP: ip("10.0.0.1"), HardwareAddr: mac("aa:bb:cc:dd:ee:02"), State: NUD_REACHABLE,
	})

	snap := s.Snapshot()
	if len(snap) != 2 {
		t.Errorf("expected 2 entries, got %d", len(snap))
	}
	key1 := NeighborKey{Dev: 1, IP: ip("10.0.0.1"), Family: syscall.AF_INET}
	key2 := NeighborKey{Dev: 2, IP: ip("10.0.0.1"), Family: syscall.AF_INET}
	if snap[key1].FlapCount != 0 || snap[key2].FlapCount != 0 {
		t.Error("different devices should be tracked independently")
	}
}

func TestHandleEvent_VRFAware(t *testing.T) {
	s := testStore(t)
	s.HandleEvent(NeighborEvent{
		Type: RTM_NEWNEIGH, LinkIndex: 1, MasterIndex: 10, Family: syscall.AF_INET,
		IP: ip("10.0.0.1"), HardwareAddr: mac("aa:bb:cc:dd:ee:01"), State: NUD_REACHABLE,
	})
	s.HandleEvent(NeighborEvent{
		Type: RTM_NEWNEIGH, LinkIndex: 1, MasterIndex: 0, Family: syscall.AF_INET,
		IP: ip("10.0.0.1"), HardwareAddr: mac("aa:bb:cc:dd:ee:02"), State: NUD_REACHABLE,
	})

	snap := s.Snapshot()
	if len(snap) != 2 {
		t.Errorf("expected 2 entries (different VRF), got %d", len(snap))
	}
}

func TestGC(t *testing.T) {
	s := testStore(t)
	s.config.StaleTTL = 1 * time.Minute

	baseTime := time.Now()
	s.now = func() time.Time { return baseTime }

	s.HandleEvent(NeighborEvent{
		Type: RTM_NEWNEIGH, LinkIndex: 1, Family: syscall.AF_INET,
		IP: ip("10.0.0.1"), HardwareAddr: mac("aa:bb:cc:dd:ee:01"), State: NUD_REACHABLE,
	})

	s.now = func() time.Time { return baseTime.Add(2 * time.Minute) }
	s.gc()

	snap := s.Snapshot()
	if len(snap) != 0 {
		t.Errorf("expected 0 entries after GC, got %d", len(snap))
	}
}

func TestDeviceFilter(t *testing.T) {
	t.Run("exclude", func(t *testing.T) {
		s := testStore(t)
		s.HandleEvent(NeighborEvent{
			Type: RTM_NEWNEIGH, LinkIndex: 1, Family: syscall.AF_INET,
			IP: ip("10.0.0.1"), HardwareAddr: mac("aa:bb:cc:dd:ee:01"), State: NUD_REACHABLE,
		})
		if len(s.Snapshot()) != 1 {
			t.Error("eth0 should be included by default")
		}
	})
}

func TestIsFlap(t *testing.T) {
	tests := []struct {
		name     string
		oldMAC   net.HardwareAddr
		newMAC   net.HardwareAddr
		expected bool
	}{
		{"both non-empty different", mac("aa:bb:cc:dd:ee:01"), mac("aa:bb:cc:dd:ee:02"), true},
		{"both non-empty same", mac("aa:bb:cc:dd:ee:01"), mac("aa:bb:cc:dd:ee:01"), false},
		{"old empty", nil, mac("aa:bb:cc:dd:ee:01"), false},
		{"new empty", mac("aa:bb:cc:dd:ee:01"), nil, false},
		{"both empty", nil, nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isFlap(tt.oldMAC, tt.newMAC); got != tt.expected {
				t.Errorf("isFlap(%v, %v) = %v, want %v", tt.oldMAC, tt.newMAC, got, tt.expected)
			}
		})
	}
}

func TestFamilyString(t *testing.T) {
	if familyString(syscall.AF_INET) != "ipv4" {
		t.Error("AF_INET should be ipv4")
	}
	if familyString(syscall.AF_INET6) != "ipv6" {
		t.Error("AF_INET6 should be ipv6")
	}
}
