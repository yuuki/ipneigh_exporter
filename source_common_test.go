package main

import (
	"context"
	"errors"
	"testing"
)

func TestNetlinkSourceHandleSubscriptionError_RecordsActiveContextError(t *testing.T) {
	var stages []string
	source := NewNetlinkSource(testLogger(), func(stage string) {
		stages = append(stages, stage)
	})

	source.handleSubscriptionError(t.Context(), errors.New("receive failed"))

	if len(stages) != 1 {
		t.Fatalf("expected one recorded error, got %d", len(stages))
	}
	if stages[0] != "netlink_receive" {
		t.Fatalf("expected netlink_receive stage, got %q", stages[0])
	}
}

func TestNetlinkSourceHandleSubscriptionError_IgnoresCanceledContextError(t *testing.T) {
	var stages []string
	source := NewNetlinkSource(testLogger(), func(stage string) {
		stages = append(stages, stage)
	})
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	source.handleSubscriptionError(ctx, errors.New("resource temporarily unavailable"))

	if len(stages) != 0 {
		t.Fatalf("expected no recorded errors after context cancellation, got %d", len(stages))
	}
}
