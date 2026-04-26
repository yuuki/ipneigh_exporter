package main

import "context"

func (s *NetlinkSource) recordStageError(stage string) {
	if s.recordError != nil {
		s.recordError(stage)
	}
}

func (s *NetlinkSource) handleSubscriptionError(ctx context.Context, err error) {
	if ctx.Err() != nil {
		s.logger.Debug("netlink subscription stopped", "error", err)
		return
	}
	s.recordStageError("netlink_receive")
	s.logger.Error("netlink subscription error", "error", err)
}
