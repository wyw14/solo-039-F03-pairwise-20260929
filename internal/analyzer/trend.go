package analyzer

import (
	"github.com/google/uuid"
	"github.com/wyw14/solo-039/internal/model"
	"math"
	"sync"
)

// SegmentReason 说明趋势段为何被切开。
type SegmentReason string

const (
	// SegmentStart 段的起点（首次采样或恢复后）。
	SegmentStart SegmentReason = "start"
	// SegmentGap 相邻样本时间间隔超过允许值，按缺测切段。
	SegmentGap SegmentReason = "gap"
	// SegmentCalibration 分析仪重新校准，旧段不可外推。
	SegmentCalibration SegmentReason = "calibration"
	// SegmentFeed 进料代际变化，趋势上下文失效。
	SegmentFeed SegmentReason = "feed"
)

// Segment 是一段连续、可比的样本序列。缺测或重新校准都会关闭当前段并另起一段，
// 因此漂移速度只能在同一段内估计，不能跨段外推。
type Segment struct {
	Calibration  uuid.UUID
	FeedRevision uuid.UUID
	Reason       SegmentReason
	StartAt      int64
	EndAt        int64
	Samples      []model.Sample
	Closed       bool
}

func (s *Segment) append(sample model.Sample) {
	if len(s.Samples) == 0 {
		s.StartAt = sample.At
	}
	s.Samples = append(s.Samples, sample)
	s.EndAt = sample.At
}

// Slope 是最小二乘拟合结果：每秒组分变化量、截距、参与拟合的样本数。
type Slope struct {
	PerSecond float64
	Intercept float64
	N         int
}

func fitSlope(samples []model.Sample) Slope {
	n := len(samples)
	if n < 2 {
		return Slope{N: n}
	}
	// 以首条样本时刻为原点，避免 1e9 量级时间戳在平方求和时浮点抵消。
	base := float64(samples[0].At)
	var sumX, sumY, sumXY, sumXX float64
	for _, sample := range samples {
		x := float64(sample.At) - base
		y := sample.Value
		sumX += x
		sumY += y
		sumXY += x * y
		sumXX += x * x
	}
	denom := float64(n)*sumXX - sumX*sumX
	if denom == 0 {
		return Slope{N: n}
	}
	slope := (float64(n)*sumXY - sumX*sumY) / denom
	intercept := (sumY - slope*sumX) / float64(n)
	return Slope{PerSecond: slope, Intercept: intercept + slope*base, N: n}
}

// pendingStart 是已宣告但尚未收到首条样本的新代际；首条样本到达时才真正成段，
// 这样“重新校准后立即绑定进料”这类连续 Begin 不会留下空段。
type pendingStart struct {
	calibration  uuid.UUID
	feedRevision uuid.UUID
	reason       SegmentReason
	at           int64
}

// Trend 维护一条按缺测/校准/进料代际切开的样本趋势。
type Trend struct {
	mu       sync.Mutex
	segments []*Segment
	pending  *pendingStart
	maxGap   int64
}

func NewTrend(maxGap int64) *Trend {
	if maxGap <= 0 {
		maxGap = 60
	}
	return &Trend{maxGap: maxGap}
}

func (t *Trend) Configure(maxGap int64) {
	t.mu.Lock()
	if maxGap > 0 {
		t.maxGap = maxGap
	}
	t.mu.Unlock()
}

// Begin 宣告新的校准/进料代际：关闭当前段，旧段保留可查；连续调用以最新代际为准。
func (t *Trend) Begin(calibration, feedRevision uuid.UUID, reason SegmentReason, at int64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.closeLocked()
	t.pending = &pendingStart{calibration: calibration, feedRevision: feedRevision, reason: reason, at: at}
}

func (t *Trend) startSegmentLocked(calibration, feedRevision uuid.UUID, reason SegmentReason, at int64) *Segment {
	segment := &Segment{
		Calibration:  calibration,
		FeedRevision: feedRevision,
		Reason:       reason,
		StartAt:      at,
		EndAt:        at,
		Samples:      make([]model.Sample, 0, 16),
	}
	t.segments = append(t.segments, segment)
	return segment
}

// Add 收录一条已通过有效性与代际校验的样本；与上一条间隔过大时先按缺测切段。
func (t *Trend) Add(sample model.Sample) {
	t.mu.Lock()
	defer t.mu.Unlock()
	// 新代际首条样本：按宣告原因（校准/进料/启动）成段，不再判定为缺测。
	if t.pending != nil {
		pending := t.pending
		t.pending = nil
		current := t.startSegmentLocked(pending.calibration, pending.feedRevision, pending.reason, sample.At)
		current.append(sample)
		return
	}
	current := t.currentLocked()
	if current == nil {
		current = t.startSegmentLocked(sample.Calibration, sample.FeedRevision, SegmentStart, sample.At)
		current.append(sample)
		return
	}
	if len(current.Samples) > 0 {
		last := current.Samples[len(current.Samples)-1]
		if sample.At-last.At > t.maxGap {
			current.Closed = true
			current = t.startSegmentLocked(sample.Calibration, sample.FeedRevision, SegmentGap, sample.At)
		}
	}
	current.append(sample)
}

func (t *Trend) currentLocked() *Segment {
	if len(t.segments) == 0 {
		return nil
	}
	return t.segments[len(t.segments)-1]
}

func (t *Trend) closeLocked() {
	if current := t.currentLocked(); current != nil {
		current.Closed = true
	}
}

// Close 关闭当前段（校准或切换进料代际时调用）。
func (t *Trend) Close() {
	t.mu.Lock()
	t.closeLocked()
	t.mu.Unlock()
}

func cloneSegment(segment *Segment) Segment {
	clone := Segment{
		Calibration:  segment.Calibration,
		FeedRevision: segment.FeedRevision,
		Reason:       segment.Reason,
		StartAt:      segment.StartAt,
		EndAt:        segment.EndAt,
		Closed:       segment.Closed,
		Samples:      append([]model.Sample(nil), segment.Samples...),
	}
	return clone
}

// Segments 返回全部趋势段（按时间顺序，含已关闭段）的副本。
func (t *Trend) Segments() []Segment {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]Segment, 0, len(t.segments))
	for _, segment := range t.segments {
		out = append(out, cloneSegment(segment))
	}
	return out
}

// CurrentSegment 返回当前（最近、未关闭）段的副本。
func (t *Trend) CurrentSegment() (Segment, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	current := t.currentLocked()
	if current == nil {
		return Segment{}, false
	}
	return cloneSegment(current), true
}

// restore 用已有序的段覆盖趋势（供进程恢复使用）。
func (t *Trend) restore(segments []Segment) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.pending = nil
	t.segments = make([]*Segment, 0, len(segments))
	for i := range segments {
		segment := segments[i]
		segment.Samples = append([]model.Sample(nil), segment.Samples...)
		t.segments = append(t.segments, &segment)
	}
}

// Identity 返回当前生效的校准/进料代际；尚无样本时以最近一次 Begin 的宣告为准。
func (t *Trend) Identity() (uuid.UUID, uuid.UUID) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.pending != nil {
		return t.pending.calibration, t.pending.feedRevision
	}
	if current := t.currentLocked(); current != nil {
		return current.Calibration, current.FeedRevision
	}
	return uuid.Nil, uuid.Nil
}

// Active 报告趋势是否已存在代际（已宣告或已有样本）。
func (t *Trend) Active() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.pending != nil || len(t.segments) > 0
}

// LastSegmentIdentity 返回最近一条实际样本段的代际（无视待生效宣告）；
// 判定“校准/进料是否变化”时应对照已有数据所属的代际，而非尚未落样本的新宣告。
func (t *Trend) LastSegmentIdentity() (uuid.UUID, uuid.UUID, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for i := len(t.segments) - 1; i >= 0; i-- {
		if len(t.segments[i].Samples) > 0 {
			segment := t.segments[i]
			return segment.Calibration, segment.FeedRevision, true
		}
	}
	return uuid.Nil, uuid.Nil, false
}

// Pending 报告是否已宣告新代际但首条样本尚未到达。
func (t *Trend) Pending() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.pending != nil
}

// SupportSample 是支撑预警结论的样本引用，便于班组核对。
type SupportSample struct {
	ID    uuid.UUID `json:"id"`
	At    int64     `json:"at"`
	Value float64   `json:"value"`
}

// SegmentTrend 是单段的漂移估计。
type SegmentTrend struct {
	Calibration  uuid.UUID      `json:"calibration"`
	FeedRevision uuid.UUID      `json:"feed_revision"`
	Reason       string         `json:"reason"`
	StartAt      int64          `json:"start_at"`
	EndAt        int64          `json:"end_at"`
	Closed       bool           `json:"closed"`
	SampleCount  int            `json:"sample_count"`
	Samples      []model.Sample `json:"samples"`
	Slope        float64        `json:"slope_per_second"`
	Intercept    float64        `json:"-"`
	Projectable  bool           `json:"projectable"`
	LatestValue  float64        `json:"latest_value"`
	LatestAt     int64          `json:"latest_at"`
}

func buildSegmentTrend(segment *Segment) SegmentTrend {
	fit := fitSlope(segment.Samples)
	report := SegmentTrend{
		Calibration:  segment.Calibration,
		FeedRevision: segment.FeedRevision,
		Reason:       string(segment.Reason),
		StartAt:      segment.StartAt,
		EndAt:        segment.EndAt,
		Closed:       segment.Closed,
		SampleCount:  len(segment.Samples),
		Samples:      append([]model.Sample(nil), segment.Samples...),
		Slope:        fit.PerSecond,
		Intercept:    fit.Intercept,
		Projectable:  fit.N >= 2,
	}
	if n := len(segment.Samples); n > 0 {
		report.LatestValue = segment.Samples[n-1].Value
		report.LatestAt = segment.Samples[n-1].At
	}
	return report
}

// ComponentLimit 是组分的合格区间。限值缺省方向时该方向不参与外推。
type ComponentLimit struct {
	Component string
	Min       float64
	Max       float64
	HasMin    bool
	HasMax    bool
}

// Crossing 是沿当前漂移方向预计越界的时刻。
type Crossing struct {
	Direction string  `json:"direction"`
	Limit     float64 `json:"limit"`
	At        int64   `json:"at"`
	Seconds   float64 `json:"seconds_from_latest"`
}

// Warning 是一条漂移预警，附支撑样本以便复核。
type Warning struct {
	Component      string          `json:"component"`
	Calibration    uuid.UUID       `json:"calibration"`
	FeedRevision   uuid.UUID       `json:"feed_revision"`
	SegmentReason  string          `json:"segment_reason"`
	Direction      string          `json:"direction"`
	Limit          float64         `json:"limit"`
	LatestValue    float64         `json:"latest_value"`
	LatestAt       int64           `json:"latest_at"`
	SlopePerSecond float64         `json:"slope_per_second"`
	CrossAt        int64           `json:"estimated_cross_at"`
	SecondsToCross float64         `json:"seconds_to_cross"`
	SampleCount    int             `json:"sample_count"`
	Support        []SupportSample `json:"support_samples"`
}

// crossingLocked 的无锁版本需在持锁场景调用；这里直接基于传入段计算。
func projectedCrossing(segment SegmentTrend, limit ComponentLimit, horizonSeconds int64) []Crossing {
	if !segment.Projectable {
		return nil
	}
	out := make([]Crossing, 0, 2)
	add := func(direction string, boundary float64) {
		// 沿漂移方向必须确实朝该限值运动，且当前尚未越过。
		if segment.Slope == 0 {
			return
		}
		delta := boundary - segment.LatestValue
		if direction == "high" {
			if segment.Slope <= 0 || delta <= 0 {
				return
			}
		} else {
			if segment.Slope >= 0 || delta >= 0 {
				return
			}
		}
		seconds := delta / segment.Slope
		if seconds < 0 {
			return
		}
		if horizonSeconds > 0 && seconds > float64(horizonSeconds) {
			return
		}
		crossAt := segment.LatestAt + int64(math.Round(seconds))
		out = append(out, Crossing{Direction: direction, Limit: boundary, At: crossAt, Seconds: seconds})
	}
	if limit.HasMax {
		add("high", limit.Max)
	}
	if limit.HasMin {
		add("low", limit.Min)
	}
	return out
}

// Report 是可查询的时间趋势结果。
type Report struct {
	Segments []SegmentTrend `json:"segments"`
	Current  *SegmentTrend  `json:"current,omitempty"`
	Warnings []Warning      `json:"warnings"`
}

// ReportOptions 控制趋势外推。
type ReportOptions struct {
	// MinSamples 给出可外推斜率的最少样本数（含），默认 2。
	MinSamples int
	// HorizonSeconds 只保留该时间窗内会越界的预警；<=0 表示不设上限。
	HorizonSeconds int64
}

// DefaultReportOptions 给出默认外推参数。
func DefaultReportOptions() ReportOptions {
	return ReportOptions{MinSamples: 2}
}

// Report 汇总各段漂移速度与当前段预警。
func (t *Trend) Report(component string, limit ComponentLimit, opts ReportOptions) Report {
	if opts.MinSamples < 2 {
		opts.MinSamples = 2
	}
	t.mu.Lock()
	segments := make([]*Segment, len(t.segments))
	copy(segments, t.segments)
	t.mu.Unlock()

	report := Report{Segments: make([]SegmentTrend, 0, len(segments)), Warnings: make([]Warning, 0)}
	for _, segment := range segments {
		report.Segments = append(report.Segments, buildSegmentTrend(segment))
	}
	if n := len(report.Segments); n > 0 && !report.Segments[n-1].Closed {
		current := report.Segments[n-1]
		report.Current = &current
	}

	if report.Current == nil || report.Current.SampleCount < opts.MinSamples {
		return report
	}
	current := report.Current
	current.Projectable = current.SampleCount >= opts.MinSamples
	for _, crossing := range projectedCrossing(*current, limit, opts.HorizonSeconds) {
		warning := Warning{
			Component:      component,
			Calibration:    current.Calibration,
			FeedRevision:   current.FeedRevision,
			SegmentReason:  current.Reason,
			Direction:      crossing.Direction,
			Limit:          crossing.Limit,
			LatestValue:    current.LatestValue,
			LatestAt:       current.LatestAt,
			SlopePerSecond: current.Slope,
			CrossAt:        crossing.At,
			SecondsToCross: crossing.Seconds,
			SampleCount:    current.SampleCount,
			Support:        make([]SupportSample, 0, current.SampleCount),
		}
		for _, sample := range current.Samples {
			warning.Support = append(warning.Support, SupportSample{ID: sample.ID, At: sample.At, Value: sample.Value})
		}
		report.Warnings = append(report.Warnings, warning)
	}
	return report
}
