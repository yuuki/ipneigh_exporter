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

	readyObserved := make(chan struct{}, 1)
	go func() {
		for {
			if len(store.Snapshot()) == 2 {
				if w.Ready() {
					readyObserved <- struct{}{}
				}
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

	select {
	case <-readyObserved:
	default:
		t.Error("watcher should be ready while processing events")
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

func TestWatcher_NotReadyUntilFirstEventProcessed(t *testing.T) {
	ch := make(chan NeighborEvent)
	w := NewWatcher(&channelSource{ch: ch}, testStore(t), testLogger())

	ctx, cancel := context.WithCancel(t.Context())
	errCh := make(chan error, 1)
	go func() {
		errCh <- w.Run(ctx)
	}()

	deadline := time.After(500 * time.Millisecond)
	for !w.Ready() {
		select {
		case <-deadline:
			goto stillNotReady
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}
	t.Fatal("watcher became ready before processing an event")

stillNotReady:
	ch <- NeighborEvent{
		Type: RTM_NEWNEIGH, LinkIndex: 1, Family: syscall.AF_INET,
		IP: ip("10.0.0.1"), HardwareAddr: mac("aa:bb:cc:dd:ee:01"),
		State: NUD_REACHABLE,
	}

	deadline = time.After(500 * time.Millisecond)
	for !w.Ready() {
		select {
		case <-deadline:
			t.Fatal("watcher did not become ready after processing an event")
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}

	cancel()

	err := <-errCh
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context cancellation, got %v", err)
	}
}

func TestWatcher_PeriodicSyncRefreshesBeforePurge(t *testing.T) {
	store := testStore(t)
	store.config.SyncInterval = 20 * time.Millisecond

	baseTime := time.Now()
	store.now = func() time.Time { return baseTime }
	store.HandleEvent(NeighborEvent{
		Type: RTM_NEWNEIGH, LinkIndex: 1, Family: syscall.AF_INET,
		IP: ip("10.0.0.1"), HardwareAddr: mac("aa:bb:cc:dd:ee:01"),
		State: NUD_STALE,
	})

	store.now = func() time.Time { return baseTime.Add(2 * time.Minute) }
	ch := make(chan NeighborEvent)
	source := &channelSource{
		ch: ch,
		list: []NeighborEvent{{
			Type: RTM_NEWNEIGH, LinkIndex: 1, Family: syscall.AF_INET,
			IP: ip("10.0.0.1"), HardwareAddr: mac("aa:bb:cc:dd:ee:01"),
			State: NUD_REACHABLE,
		}},
	}
	w := NewWatcher(source, store, testLogger())

	ctx, cancel := context.WithCancel(t.Context())
	errCh := make(chan error, 1)
	go func() {
		errCh <- w.Run(ctx)
	}()

	deadline := time.After(500 * time.Millisecond)
	for {
		snap := store.Snapshot()
		entry := snap[NeighborKey{Dev: 1, IP: ip("10.0.0.1"), Family: syscall.AF_INET}]
		if entry != nil && entry.State == NUD_REACHABLE {
			break
		}
		select {
		case <-deadline:
			t.Fatal("entry was not refreshed by periodic sync")
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}

	cancel()
	if err := <-errCh; !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context cancellation, got %v", err)
	}
}

func TestWatcher_ProcessesEventsWhilePeriodicSyncIsRunning(t *testing.T) {
	store := testStore(t)
	store.config.SyncInterval = 20 * time.Millisecond
	source := newBlockingListSource()
	w := NewWatcher(source, store, testLogger())

	ctx, cancel := context.WithCancel(t.Context())
	errCh := make(chan error, 1)
	go func() {
		errCh <- w.Run(ctx)
	}()

	select {
	case <-source.listStarted:
	case <-time.After(500 * time.Millisecond):
		cancel()
		t.Fatal("periodic sync did not start")
	}

	source.ch <- NeighborEvent{
		Type: RTM_NEWNEIGH, LinkIndex: 1, Family: syscall.AF_INET,
		IP: ip("10.0.0.1"), HardwareAddr: mac("aa:bb:cc:dd:ee:01"),
		State: NUD_REACHABLE,
	}

	deadline := time.After(500 * time.Millisecond)
	for len(store.Snapshot()) == 0 {
		select {
		case <-deadline:
			close(source.unblockList)
			cancel()
			t.Fatal("watcher did not process event while periodic sync was running")
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}

	close(source.unblockList)
	cancel()
	if err := <-errCh; !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context cancellation, got %v", err)
	}
}

func TestWatcher_SyncErrorDoesNotPurge(t *testing.T) {
	store := testStore(t)
	store.config.SyncInterval = 20 * time.Millisecond

	baseTime := time.Now()
	store.now = func() time.Time { return baseTime }
	store.HandleEvent(NeighborEvent{
		Type: RTM_NEWNEIGH, LinkIndex: 1, Family: syscall.AF_INET,
		IP: ip("10.0.0.1"), HardwareAddr: mac("aa:bb:cc:dd:ee:01"),
		State: NUD_STALE,
	})

	store.now = func() time.Time { return baseTime.Add(2 * time.Minute) }
	ch := make(chan NeighborEvent)
	w := NewWatcher(&channelSource{ch: ch, listErr: errors.New("list failed")}, store, testLogger())

	ctx, cancel := context.WithCancel(t.Context())
	errCh := make(chan error, 1)
	go func() {
		errCh <- w.Run(ctx)
	}()

	deadline := time.After(500 * time.Millisecond)
	for counterValue(store.errorsTotal, "netlink_list") == 0 {
		select {
		case <-deadline:
			t.Fatal("expected netlink_list error to be recorded")
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}

	if len(store.Snapshot()) != 1 {
		t.Fatal("sync failure should not purge stale entries")
	}

	cancel()
	if err := <-errCh; !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context cancellation, got %v", err)
	}
}

func TestWatcher_PartialSyncFailureDoesNotPurge(t *testing.T) {
	store := testStore(t)
	store.config.SyncInterval = 20 * time.Millisecond

	baseTime := time.Now()
	store.now = func() time.Time { return baseTime }
	store.HandleEvent(NeighborEvent{
		Type: RTM_NEWNEIGH, LinkIndex: 1, Family: syscall.AF_INET,
		IP: ip("10.0.0.1"), HardwareAddr: mac("aa:bb:cc:dd:ee:01"),
		State: NUD_STALE,
	})

	store.now = func() time.Time { return baseTime.Add(2 * time.Minute) }
	ch := make(chan NeighborEvent)
	source := &channelSource{
		ch: ch,
		list: []NeighborEvent{{
			Type: RTM_NEWNEIGH, LinkIndex: 99, Family: syscall.AF_INET,
			IP: ip("10.0.0.99"), HardwareAddr: mac("aa:bb:cc:dd:ee:99"),
			State: NUD_REACHABLE,
		}},
	}
	w := NewWatcher(source, store, testLogger())

	ctx, cancel := context.WithCancel(t.Context())
	errCh := make(chan error, 1)
	go func() {
		errCh <- w.Run(ctx)
	}()

	deadline := time.After(500 * time.Millisecond)
	for counterValue(store.errorsTotal, "link_resolve") == 0 {
		select {
		case <-deadline:
			t.Fatal("expected link_resolve error to be recorded")
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}

	if len(store.Snapshot()) != 1 {
		t.Fatal("partial sync failure should not purge stale entries")
	}

	cancel()
	if err := <-errCh; !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context cancellation, got %v", err)
	}
}
