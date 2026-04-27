package main

import (
	"context"
	"errors"
)

type mockSource struct {
	events []NeighborEvent
	list   []NeighborEvent
	err    error
}

func (m *mockSource) Subscribe(_ context.Context) (<-chan NeighborEvent, error) {
	ch := make(chan NeighborEvent, len(m.events))
	for _, ev := range m.events {
		ch <- ev
	}
	close(ch)
	return ch, nil
}

func (m *mockSource) List(_ context.Context) ([]NeighborEvent, error) {
	if m.err != nil {
		return nil, m.err
	}
	return m.list, nil
}

type closedSource struct{}

func (s *closedSource) Subscribe(_ context.Context) (<-chan NeighborEvent, error) {
	ch := make(chan NeighborEvent)
	close(ch)
	return ch, nil
}

func (s *closedSource) List(_ context.Context) ([]NeighborEvent, error) {
	return nil, nil
}

type errorSource struct {
	err error
}

func (s *errorSource) Subscribe(_ context.Context) (<-chan NeighborEvent, error) {
	if s.err == nil {
		s.err = errors.New("subscribe failed")
	}
	return nil, s.err
}

func (s *errorSource) List(_ context.Context) ([]NeighborEvent, error) {
	return nil, nil
}

type channelSource struct {
	ch      <-chan NeighborEvent
	list    []NeighborEvent
	listErr error
}

func (s *channelSource) Subscribe(_ context.Context) (<-chan NeighborEvent, error) {
	return s.ch, nil
}

func (s *channelSource) List(_ context.Context) ([]NeighborEvent, error) {
	if s.listErr != nil {
		return nil, s.listErr
	}
	return s.list, nil
}

type mockResolver struct {
	names map[int]string
}

func (r *mockResolver) LinkName(index int) (string, error) {
	if name, ok := r.names[index]; ok {
		return name, nil
	}
	return "", nil
}

type countingResolver struct {
	names map[int]string
	calls map[int]int
}

func (r *countingResolver) LinkName(index int) (string, error) {
	if r.calls == nil {
		r.calls = make(map[int]int)
	}
	r.calls[index]++
	if name, ok := r.names[index]; ok {
		return name, nil
	}
	return "", nil
}

type blockingListSource struct {
	ch          chan NeighborEvent
	listStarted chan struct{}
	unblockList chan struct{}
}

func newBlockingListSource() *blockingListSource {
	return &blockingListSource{
		ch:          make(chan NeighborEvent, 1),
		listStarted: make(chan struct{}),
		unblockList: make(chan struct{}),
	}
}

func (s *blockingListSource) Subscribe(_ context.Context) (<-chan NeighborEvent, error) {
	return s.ch, nil
}

func (s *blockingListSource) List(ctx context.Context) ([]NeighborEvent, error) {
	select {
	case <-s.listStarted:
	default:
		close(s.listStarted)
	}
	select {
	case <-s.unblockList:
		return nil, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
