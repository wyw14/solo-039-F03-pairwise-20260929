package analyzer

import (
	"github.com/google/uuid"
	"github.com/wyw14/solo-039/internal/model"
)

type RecoveryState struct {
	Calibration  uuid.UUID
	FeedRevision uuid.UUID
	Samples      []model.Sample
	Segments     []TrendSegment
}

func Capture(state *State) RecoveryState {
	calibration, feedRevision := state.window.Identity()
	return RecoveryState{
		Calibration:  calibration,
		FeedRevision: feedRevision,
		Samples:      state.window.Samples(),
		Segments:     state.trends.Segments(),
	}
}

func Restore(state *State, recovery RecoveryState) {
	state.mu.Lock()
	state.calibration = recovery.Calibration
	state.window.Reset(recovery.Calibration, recovery.FeedRevision)
	state.trends.Restore(recovery.Segments)
	window := state.window
	state.mu.Unlock()
	// 仅回填即时证明窗口；趋势段已整体恢复，不能把这些样本再追加一遍。
	for _, sample := range recovery.Samples {
		window.Add(sample)
	}
}

func Compatible(recovery RecoveryState, calibration, feedRevision uuid.UUID) bool {
	return recovery.Calibration == calibration && recovery.FeedRevision == feedRevision
}
