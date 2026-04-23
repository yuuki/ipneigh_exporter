package main

import (
	"context"
	"errors"
)

type mockSource struct {
	events []NeighborEvent
}

func (m *mockSource) Subscribe(_ context.Context) (<-chan NeighborEvent, error) {
	ch := make(chan NeighborEvent, len(m.events))
	for _, ev := range m.events {
		ch <- ev
	}
	close(ch)
	return ch, nil
}

type closedSource struct{}

func (s *closedSource) Subscribe(_ context.Context) (<-chan NeighborEvent, error) {
	ch := make(chan NeighborEvent)
	close(ch)
	return ch, nil
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

type channelSource struct {
	ch <-chan NeighborEvent
}

func (s *channelSource) Subscribe(_ context.Context) (<-chan NeighborEvent, error) {
	return s.ch, nil
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
