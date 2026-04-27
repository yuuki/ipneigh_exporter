package main

import (
	"context"
	"errors"
	"log/slog"
	"sync/atomic"
	"time"
)

var errNeighborEventChannelClosed = errors.New("neighbor event channel closed")

type Watcher struct {
	source NeighborSource
	store  *NeighborStore
	ready  atomic.Bool
	logger *slog.Logger
}

func NewWatcher(source NeighborSource, store *NeighborStore, logger *slog.Logger) *Watcher {
	return &Watcher{
		source: source,
		store:  store,
		logger: logger,
	}
}

func (w *Watcher) Run(ctx context.Context) error {
	ch, err := w.source.Subscribe(ctx)
	if err != nil {
		return err
	}
	defer w.ready.Store(false)

	w.logger.Info("watching neighbor events")

	var syncC <-chan time.Time
	var syncTicker *time.Ticker
	if w.store.config.SyncInterval > 0 {
		syncTicker = time.NewTicker(w.store.config.SyncInterval)
		defer syncTicker.Stop()
		syncC = syncTicker.C
	}

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-syncC:
			events, err := w.source.List(ctx)
			if err != nil {
				w.store.RecordError("netlink_list")
				w.logger.Error("neighbor sync failed", "error", err)
				continue
			}
			if w.store.SyncNeighbors(events) {
				w.store.gc()
			}
			w.ready.Store(true)
		case ev, ok := <-ch:
			if !ok {
				w.logger.Warn("neighbor event channel closed")
				return errNeighborEventChannelClosed
			}
			w.store.HandleEvent(ev)
			w.ready.Store(true)
		}
	}
}

func (w *Watcher) Ready() bool {
	return w.ready.Load()
}
