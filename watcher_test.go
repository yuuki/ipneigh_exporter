package main

import (
	"context"
	"syscall"
	"testing"
	"time"
)

func TestWatcher_ProcessesEvents(t *testing.T) {
	store := testStore(t)

	source := &mockSource{
		events: []NeighborEvent{
			{
				Type: RTM_NEWNEIGH, LinkIndex: 1, Family: syscall.AF_INET,
				IP: ip("10.0.0.1"), HardwareAddr: mac("aa:bb:cc:dd:ee:01"),
				State: NUD_REACHABLE,
			},
			{
				Type: RTM_NEWNEIGH, LinkIndex: 1, Family: syscall.AF_INET,
				IP: ip("10.0.0.2"), HardwareAddr: mac("aa:bb:cc:dd:ee:02"),
				State: NUD_STALE,
			},
		},
	}

	w := NewWatcher(source, store, testLogger())

	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()

	err := w.Run(ctx)
	if err != nil {
		t.Fatalf("watcher.Run error: %v", err)
	}

	snap := store.Snapshot()
	if len(snap) != 2 {
		t.Errorf("expected 2 entries, got %d", len(snap))
	}

	if !w.Ready() {
		t.Error("watcher should be ready after processing events")
	}
}
