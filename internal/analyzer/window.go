package analyzer

import (
	"github.com/google/uuid"
	"github.com/wyw14/solo-039/internal/model"
	"sync"
)

// Window 同时承担两件事：
//   - 当前段样本作为即时证明窗口（Complete/Samples 的既有语义不变）；
//   - 底层 Trend 保留按缺测、重新校准、进料切换切开的历史段供时间趋势查询。
type Window struct {
	mu     sync.Mutex
	trend  *Trend
	needed int
	maxGap int64
}

func NewWindow() *Window {
	return &Window{trend: NewTrend(60), needed: 3, maxGap: 60}
}

// Reset 宣告新的校准/进料代际，关闭当前证明窗口；趋势旧段保留可查。
func (w *Window) Reset(calibration, feedRevision uuid.UUID) {
	w.mu.Lock()
	reason := SegmentStart
	if prevCalibration, prevFeed, ok := w.trend.LastSegmentIdentity(); ok {
		switch {
		case calibration != prevCalibration:
			reason = SegmentCalibration
		case feedRevision != prevFeed:
			reason = SegmentFeed
		}
	}
	w.trend.Begin(calibration, feedRevision, reason, 0)
	w.mu.Unlock()
}

func (w *Window) Add(sample model.Sample) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	calibration, feedRevision := w.trend.Identity()
	if !sample.Valid || sample.Calibration != calibration || sample.FeedRevision != feedRevision {
		return false
	}
	// 已宣告新代际（校准/进料切换）时，首条样本开启新段，不与旧段比较时间。
	if !w.trend.Pending() {
		if current, ok := w.trend.CurrentSegment(); ok && len(current.Samples) > 0 {
			last := current.Samples[len(current.Samples)-1]
			if sample.At <= last.At {
				return false
			}
		}
	}
	// 与上一条样本间隔超过 maxGap 时，Trend 内部会先按缺测切段再收录。
	w.trend.Add(sample)
	return true
}

func (w *Window) Configure(needed int, maxGap int64) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if needed > 0 {
		w.needed = needed
	}
	if maxGap > 0 {
		w.maxGap = maxGap
		w.trend.Configure(maxGap)
	}
}

func (w *Window) Identity() (uuid.UUID, uuid.UUID) {
	return w.trend.Identity()
}

func (w *Window) Complete() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	current, ok := w.trend.CurrentSegment()
	return ok && len(current.Samples) >= w.needed
}

// Samples 返回当前证明窗口（当前趋势段）的样本副本。
func (w *Window) Samples() []model.Sample {
	w.mu.Lock()
	defer w.mu.Unlock()
	current, ok := w.trend.CurrentSegment()
	if !ok {
		return []model.Sample{}
	}
	return append([]model.Sample(nil), current.Samples...)
}

func (w *Window) Trend() *Trend { return w.trend }
