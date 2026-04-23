package main

import "context"

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

type mockResolver struct {
	names map[int]string
}

func (r *mockResolver) LinkName(index int) (string, error) {
	if name, ok := r.names[index]; ok {
		return name, nil
	}
	return "", nil
}
