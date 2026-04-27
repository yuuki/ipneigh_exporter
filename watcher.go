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
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	defer w.ready.Store(false)

	w.logger.Info("watching neighbor events")

	if w.store.config.SyncInterval > 0 {
		go w.runPeriodicSync(ctx)
	}

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
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

func (w *Watcher) runPeriodicSync(ctx context.Context) {
	ticker := time.NewTicker(w.store.config.SyncInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.syncOnce(ctx)
		}
	}
}

func (w *Watcher) syncOnce(ctx context.Context) {
	syncTime := w.store.now()
	events, err := w.source.List(ctx)
	if err != nil {
		w.store.RecordError("netlink_list")
		w.logger.Error("neighbor sync failed", "error", err)
		return
	}
	if w.store.syncNeighborsAt(events, syncTime) {
		w.store.gcAt(syncTime)
	}
	w.ready.Store(true)
}

func (w *Watcher) Ready() bool {
	return w.ready.Load()
}
