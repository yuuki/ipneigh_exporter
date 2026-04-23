package main

import (
	"context"
	"errors"
	"syscall"
	"testing"
	"time"
)

func TestWatcher_ProcessesEvents(t *testing.T) {
	store := testStore(t)

	ch := make(chan NeighborEvent, 2)
	ch <- NeighborEvent{
		Type: RTM_NEWNEIGH, LinkIndex: 1, Family: syscall.AF_INET,
		IP: ip("10.0.0.1"), HardwareAddr: mac("aa:bb:cc:dd:ee:01"),
		State: NUD_REACHABLE,
	}
	ch <- NeighborEvent{
		Type: RTM_NEWNEIGH, LinkIndex: 1, Family: syscall.AF_INET,
		IP: ip("10.0.0.2"), HardwareAddr: mac("aa:bb:cc:dd:ee:02"),
		State: NUD_STALE,
	}
	source := &channelSource{ch: ch}

	w := NewWatcher(source, store, testLogger())

	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()

	go func() {
		for {
			if len(store.Snapshot()) == 2 {
				cancel()
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	}()

	err := w.Run(ctx)
	if !errors.Is(err, context.Canceled) {
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

func TestWatcher_ChannelClosed_ReturnsError(t *testing.T) {
	w := NewWatcher(&closedSource{}, testStore(t), testLogger())

	err := w.Run(t.Context())
	if err == nil {
		t.Fatal("expected error when source channel closes")
	}
}

func TestWatcher_SubscribeError_ReturnsError(t *testing.T) {
	want := errors.New("boom")
	w := NewWatcher(&errorSource{err: want}, testStore(t), testLogger())

	err := w.Run(t.Context())
	if !errors.Is(err, want) {
		t.Fatalf("expected %v, got %v", want, err)
	}
}
