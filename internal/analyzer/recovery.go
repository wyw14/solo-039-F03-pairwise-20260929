package analyzer

import (
	"github.com/google/uuid"
	"github.com/wyw14/solo-039/internal/model"
)

type RecoveryState struct {
	Calibration  uuid.UUID
	FeedRevision uuid.UUID
	Samples      []model.Sample
	Segments     []Segment
	Component    string
	Limit        ComponentLimit
	Report       ReportOptions
}

func Capture(state *State) RecoveryState {
	calibration, feedRevision := state.window.Identity()
	segments := state.window.Trend().Segments()
	state.mu.RLock()
	component := state.component
	limit := state.limit
	report := state.report
	state.mu.RUnlock()
	return RecoveryState{
		Calibration:  calibration,
		FeedRevision: feedRevision,
		Samples:      state.window.Samples(),
		Segments:     segments,
		Component:    component,
		Limit:        limit,
		Report:       report,
	}
}

func Restore(state *State, recovery RecoveryState) {
	state.mu.Lock()
	state.calibration = recovery.Calibration
	if recovery.Component != "" {
		state.component = recovery.Component
	}
	state.limit = recovery.Limit
	if recovery.Report.MinSamples > 0 || recovery.Report.HorizonSeconds > 0 {
		state.report = recovery.Report
	}
	state.mu.Unlock()

	if len(recovery.Segments) > 0 {
		state.window.Trend().restore(recovery.Segments)
		// 快照可能正处于“已宣告新代际、首条样本尚未到达”：末段身份与恢复身份不一致时重建待生效段。
		gotCalibration, gotFeed := state.window.Identity()
		if gotCalibration != recovery.Calibration || gotFeed != recovery.FeedRevision {
			state.window.Reset(recovery.Calibration, recovery.FeedRevision)
		}
	} else {
		state.window.Reset(recovery.Calibration, recovery.FeedRevision)
		for _, sample := range recovery.Samples {
			state.Add(sample)
		}
	}
}

func Compatible(recovery RecoveryState, calibration, feedRevision uuid.UUID) bool {
	return recovery.Calibration == calibration && recovery.FeedRevision == feedRevision
}
