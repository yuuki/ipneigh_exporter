package main

import (
	"context"
	"errors"
	"log/slog"
	"sync/atomic"
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

	w.logger.Info("watching neighbor events")

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case ev, ok := <-ch:
			if !ok {
				w.logger.Warn("neighbor event channel closed")
				return errNeighborEventChannelClosed
			}
			w.ready.Store(true)
			w.store.HandleEvent(ev)
		}
	}
}

func (w *Watcher) Ready() bool {
	return w.ready.Load()
}
