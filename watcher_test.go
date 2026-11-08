package main

import (
	"context"
	"errors"
	"syscall"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"go.uber.org/goleak"
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

// Run owns the periodic sync goroutine: it must stop once Run returns because
// ctx ended, even while it is waiting in List.
func TestWatcher_RunStopsPeriodicSyncOnCancel(t *testing.T) {
	defer goleak.VerifyNone(t)

	store := testStore(t)
	store.config.SyncInterval = time.Millisecond
	source := newBlockingListSource()
	w := NewWatcher(source, store, testLogger())

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	errCh := make(chan error, 1)
	go func() { errCh <- w.Run(ctx) }()

	select {
	case <-source.listStarted:
	case <-time.After(3 * time.Second):
		t.Fatal("periodic sync did not start")
	}
	cancel()
	select {
	case err := <-errCh:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected context cancellation, got %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not return after cancellation")
	}
}

// When the source closes its channel, Run applies what was delivered before
// the close, returns errNeighborEventChannelClosed and stops the periodic sync.
func TestWatcher_RunStopsPeriodicSyncWhenSourceCloses(t *testing.T) {
	defer goleak.VerifyNone(t)

	store := testStore(t)
	store.config.SyncInterval = time.Millisecond
	events := []NeighborEvent{
		{Type: RTM_NEWNEIGH, LinkIndex: 1, Family: syscall.AF_INET, IP: ip("10.0.0.1"), HardwareAddr: mac("aa:bb:cc:dd:ee:01"), State: NUD_REACHABLE},
		{Type: RTM_NEWNEIGH, LinkIndex: 2, Family: syscall.AF_INET6, IP: ip("2001:db8::1"), HardwareAddr: mac("aa:bb:cc:dd:ee:02"), State: NUD_STALE},
	}
	w := NewWatcher(&mockSource{events: events, list: events}, store, testLogger())

	done := make(chan error, 1)
	go func() { done <- w.Run(t.Context()) }()
	select {
	case err := <-done:
		if !errors.Is(err, errNeighborEventChannelClosed) {
			t.Fatalf("expected errNeighborEventChannelClosed, got %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not return after the source closed its channel")
	}
	if got := len(store.Snapshot()); got != len(events) {
		t.Fatalf("expected %d entries from the delivered events, got %d", len(events), got)
	}
}

// NetlinkSource closes its channel when ctx ends, so at shutdown Run can find
// both the cancellation and the closed channel ready; it must report the
// cancellation, not a failed source (which also logs a warning).
func TestWatcher_CancellationWinsOverSourceClose(t *testing.T) {
	for i := range 100 {
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		w := NewWatcher(&closedSource{}, testStore(t), testLogger())
		if err := w.Run(ctx); !errors.Is(err, context.Canceled) {
			t.Fatalf("run %d: expected context cancellation, got %v", i, err)
		}
	}
}

// A periodic sync lists the kernel table while Run keeps handling events, so
// an event handled while the dump is in flight is newer than the dump. The
// stale dump entry must not undo it: applied afterwards, it would count a flap
// back to the old MAC (and the next event a third one for a single change), or
// bring back an entry the kernel deleted.
func TestWatcher_SyncDoesNotApplyDumpOlderThanEvents(t *testing.T) {
	const macA, macB, macC = "aa:bb:cc:dd:ee:01", "aa:bb:cc:dd:ee:02", "aa:bb:cc:dd:ee:03"
	key := NeighborKey{Dev: 1, IP: ip("10.0.0.1"), Family: syscall.AF_INET}
	newNeigh := func(hw string) NeighborEvent {
		return NeighborEvent{Type: RTM_NEWNEIGH, LinkIndex: 1, Family: syscall.AF_INET, IP: key.IP, HardwareAddr: mac(hw), State: NUD_REACHABLE}
	}
	flaps := func(s *NeighborStore) float64 {
		return counterValue(s.flapCounter, "eth0", "", "10.0.0.1", "ipv4")
	}

	t.Run("MAC change while listing", func(t *testing.T) {
		store := testStore(t)
		store.HandleEvent(newNeigh(macA))
		// The dump still shows A; the kernel moves the IP to B and that
		// event is handled before the dump is applied: one real change.
		source := &channelSource{list: []NeighborEvent{newNeigh(macA)}}
		source.duringList = func() { store.HandleEvent(newNeigh(macB)) }
		w := NewWatcher(source, store, testLogger())

		w.syncOnce(t.Context())
		if e := store.Snapshot()[key]; e == nil || e.MAC.String() != macB || e.FlapCount != 1 {
			t.Fatalf("after the sync: entry %+v, want MAC %s and 1 flap", e, macB)
		}
		store.HandleEvent(newNeigh(macB))
		if got := flaps(store); got != 1 {
			t.Fatalf("one MAC change counted %v flaps, want 1", got)
		}

		// A sync that starts listing after the event applies its dump.
		source.duringList = nil
		source.list = []NeighborEvent{newNeigh(macC)}
		w.syncOnce(t.Context())
		if e := store.Snapshot()[key]; e == nil || e.MAC.String() != macC || flaps(store) != 2 {
			t.Fatalf("after a current sync: entry %+v and %v flaps, want MAC %s and 2 flaps", e, flaps(store), macC)
		}
	})

	t.Run("delete while listing", func(t *testing.T) {
		store := testStore(t)
		store.HandleEvent(newNeigh(macA))
		source := &channelSource{list: []NeighborEvent{newNeigh(macA)}}
		source.duringList = func() {
			store.HandleEvent(NeighborEvent{Type: RTM_DELNEIGH, LinkIndex: 1, Family: syscall.AF_INET, IP: key.IP, State: NUD_FAILED})
		}
		w := NewWatcher(source, store, testLogger())

		w.syncOnce(t.Context())
		if e := store.Snapshot()[key]; e == nil || !e.Deleted || e.State != NUD_FAILED {
			t.Fatalf("after the sync: entry %+v, want it deleted in state failed", e)
		}
		if n := testutil.CollectAndCount(NewNeighborCollector(store), "ipneigh_entries"); n != 0 {
			t.Fatalf("ipneigh_entries has %d series, want none for a deleted entry", n)
		}
	})
}
