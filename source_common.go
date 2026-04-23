package main

func (s *NetlinkSource) recordStageError(stage string) {
	if s.recordError != nil {
		s.recordError(stage)
	}
}
