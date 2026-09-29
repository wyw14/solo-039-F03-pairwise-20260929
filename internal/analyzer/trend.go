package analyzer

import (
	"github.com/google/uuid"
	"github.com/wyw14/solo-039/internal/model"
	"math"
	"sync"
)

// TrendSegment 是一段连续、可比的样本趋势。
// 缺测（相邻样本时间差超过 maxGap）、重新校准或切换进料时必须切段，
// 因为这些事件之后的数值与段内历史不再属于同一基准。
type TrendSegment struct {
	ID           uuid.UUID      `json:"id"`
	Calibration  uuid.UUID      `json:"calibration"`
	FeedRevision uuid.UUID      `json:"feed_revision"`
	StartedAt    int64          `json:"started_at"`
	EndedAt      int64          `json:"ended_at"`
	Closed       bool           `json:"closed"`
	Samples      []model.Sample `json:"samples"`
}

// Trend 是对一个趋势段做线性拟合的结果。时间以秒为单位，故漂移速度单位为“每 秒”。
type Trend struct {
	SegmentID    uuid.UUID `json:"segment_id"`
	Calibration  uuid.UUID `json:"calibration"`
	FeedRevision uuid.UUID `json:"feed_revision"`
	StartedAt    int64     `json:"started_at"`
	EndedAt      int64     `json:"ended_at"`
	SampleCount  int       `json:"sample_count"`
	Latest       float64   `json:"latest"`
	Slope        float64   `json:"slope_per_second"`
	Intercept    float64   `json:"intercept"`
	R2           float64   `json:"r_squared"`
	Limit        float64   `json:"limit"`
	Breached     bool      `json:"breached"`
	// EstimatedAt > 0 时为按当前漂移速度线性外推的预计越界时刻（Unix 秒）。
	EstimatedAt int64 `json:"estimated_breach_at,omitempty"`
	// SecondsAhead > 0 时为距预计越界还剩多少秒；-1 表示按当前速度不会越界；0 表示无法估计。
	SecondsAhead int64 `json:"seconds_ahead"`
}

// TrendWarning 是一条提前预警，附带支撑该结论的全部样本，便于班组核对。
type TrendWarning struct {
	Trend   Trend          `json:"trend"`
	Level   string         `json:"level"`
	Message string         `json:"message"`
	Samples []model.Sample `json:"supporting_samples"`
}

// TrendConfig 控制趋势拟合与预警门槛。
type TrendConfig struct {
	// MaxGap 与窗口缺测阈值一致，超过即切段。
	MaxGap int64
	// MinSamples 至少需要多少样本才拟合漂移速度（含端点的最小二乘至少 2 个）。
	MinSamples int
	// Limit 为组分限值；值逼近或越过该限值即关注。
	Limit float64
	// WarnHorizon 在多少秒内预计越界即发出预警。
	WarnHorizon int64
	// MaxSegments 趋势段保留上限（按开始时间最旧的丢弃）。
	MaxSegments int
}

func DefaultTrendConfig() TrendConfig {
	return TrendConfig{MaxGap: 60, MinSamples: 2, Limit: 0.95, WarnHorizon: 600, MaxSegments: 50}
}

// TrendBook 记录可查询的采样时间趋势，与即时证明链（Window/Quality）完全独立。
type TrendBook struct {
	mu     sync.Mutex
	cfg    TrendConfig
	open   *TrendSegment
	closed []TrendSegment
}

func NewTrendBook(cfg TrendConfig) *TrendBook {
	if cfg.MaxGap <= 0 {
		cfg.MaxGap = 60
	}
	if cfg.MinSamples < 2 {
		cfg.MinSamples = 2
	}
	if cfg.WarnHorizon <= 0 {
		cfg.WarnHorizon = 600
	}
	if cfg.MaxSegments <= 0 {
		cfg.MaxSegments = 50
	}
	return &TrendBook{cfg: cfg, closed: make([]TrendSegment, 0, cfg.MaxSegments)}
}

// Add 追加一条有效样本；基准变化或缺测会自动切开新段。
// 返回 false 表示样本不属于当前基准（无效样本、其它校准或进料修订）。
func (b *TrendBook) Add(sample model.Sample) bool {
	if !sample.Valid {
		return false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.open == nil {
		b.open = b.newSegment(sample)
	} else {
		if sample.Calibration != b.open.Calibration || sample.FeedRevision != b.open.FeedRevision {
			return false
		}
		if sample.At <= b.open.EndedAt {
			return false
		}
		if sample.At-b.open.EndedAt > b.cfg.MaxGap {
			b.closeLocked()
			b.open = b.newSegment(sample)
		}
	}
	b.open.Samples = append(b.open.Samples, sample)
	b.open.EndedAt = sample.At
	return true
}

// Split 因外部事件（重新校准、切换进料）显式切开当前趋势段。
func (b *TrendBook) Split() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.closeLocked()
}

func (b *TrendBook) newSegment(first model.Sample) *TrendSegment {
	return &TrendSegment{
		ID:           uuid.New(),
		Calibration:  first.Calibration,
		FeedRevision: first.FeedRevision,
		StartedAt:    first.At,
		EndedAt:      first.At,
		Samples:      make([]model.Sample, 0, 16),
	}
}

func (b *TrendBook) closeLocked() {
	if b.open == nil {
		return
	}
	b.open.Closed = true
	b.closed = append(b.closed, *b.open)
	if len(b.closed) > b.cfg.MaxSegments {
		b.closed = b.closed[len(b.closed)-b.cfg.MaxSegments:]
	}
	b.open = nil
}

// Segments 返回全部趋势段（最新的在后），按时间排序。
func (b *TrendBook) Segments() []TrendSegment {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]TrendSegment, 0, len(b.closed)+1)
	out = append(out, b.closed...)
	if b.open != nil {
		out = append(out, *b.open)
	}
	for i := range out {
		out[i].Samples = append([]model.Sample(nil), out[i].Samples...)
	}
	return out
}

// CurrentSegment 返回当前尚未闭合的趋势段副本；不存在时 ok 为 false。
func (b *TrendBook) CurrentSegment() (TrendSegment, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.open == nil {
		return TrendSegment{}, false
	}
	seg := *b.open
	seg.Samples = append([]model.Sample(nil), seg.Samples...)
	return seg, true
}

func (b *TrendBook) config() TrendConfig {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.cfg
}

// Configure 仅更新给出的正值项，并可同步调整缺测阈值；已有趋势段保留。
func (b *TrendBook) Configure(cfg TrendConfig) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if cfg.MaxGap > 0 {
		b.cfg.MaxGap = cfg.MaxGap
	}
	if cfg.MinSamples >= 2 {
		b.cfg.MinSamples = cfg.MinSamples
	}
	if cfg.Limit > 0 {
		b.cfg.Limit = cfg.Limit
	}
	if cfg.WarnHorizon > 0 {
		b.cfg.WarnHorizon = cfg.WarnHorizon
	}
	if cfg.MaxSegments > 0 {
		b.cfg.MaxSegments = cfg.MaxSegments
		if len(b.closed) > b.cfg.MaxSegments {
			b.closed = b.closed[len(b.closed)-b.cfg.MaxSegments:]
		}
	}
}

func (b *TrendBook) Config() TrendConfig {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.cfg
}

// Restore 从持久化的趋势段重建账本，最后一个未闭合段恢复为开放段。
func (b *TrendBook) Restore(segments []TrendSegment) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.closed = b.closed[:0]
	b.open = nil
	for i := range segments {
		seg := segments[i]
		if len(seg.Samples) == 0 {
			continue
		}
		if !seg.Closed && i == len(segments)-1 {
			b.open = &seg
			continue
		}
		if !seg.Closed {
			seg.Closed = true
		}
		b.closed = append(b.closed, seg)
	}
	if len(b.closed) > b.cfg.MaxSegments {
		b.closed = b.closed[len(b.closed)-b.cfg.MaxSegments:]
	}
}

// Fit 对单个趋势段做最小二乘线性拟合并外推越界时间。
func Fit(seg TrendSegment, cfg TrendConfig) Trend {
	trend := Trend{
		SegmentID:    seg.ID,
		Calibration:  seg.Calibration,
		FeedRevision: seg.FeedRevision,
		StartedAt:    seg.StartedAt,
		EndedAt:      seg.EndedAt,
		SampleCount:  len(seg.Samples),
		Limit:        cfg.Limit,
		SecondsAhead: 0,
	}
	n := len(seg.Samples)
	if n == 0 {
		return trend
	}
	latest := seg.Samples[n-1]
	trend.Latest = latest.Value
	if latest.Value >= cfg.Limit {
		trend.Breached = true
	}
	if n < cfg.MinSamples {
		trend.SecondsAhead = -1
		return trend
	}

	// 以首样本时刻为时间原点做 OLS：value = slope*(t-t0) + intercept。
	t0 := float64(seg.Samples[0].At)
	var sumX, sumY, sumXY, sumXX, sumYY float64
	for _, s := range seg.Samples {
		x := float64(s.At) - t0
		y := s.Value
		sumX += x
		sumY += y
		sumXY += x * y
		sumXX += x * x
		sumYY += y * y
	}
	fn := float64(n)
	denom := fn*sumXX - sumX*sumX
	if denom == 0 {
		// 所有样本时刻相同，无法拟合。
		trend.SecondsAhead = -1
		return trend
	}
	slope := (fn*sumXY - sumX*sumY) / denom
	intercept := (sumY - slope*sumX) / fn
	trend.Slope = slope
	trend.Intercept = intercept
	if ss := fn*sumYY - sumY*sumY; ss > 0 {
		num := fn*sumXY - sumX*sumY
		trend.R2 = (num * num) / (denom * ss)
		if trend.R2 > 1 {
			trend.R2 = 1
		}
	} else {
		trend.R2 = 1 // 所有取值相同，完全“拟合”。
	}

	// 只有朝限值方向（值随时间增大）漂移才谈得上预计越界。
	if slope <= 0 {
		trend.SecondsAhead = -1
		return trend
	}
	xBreach := (cfg.Limit - intercept) / slope
	if xBreach < 0 {
		xBreach = 0
	}
	estimated := int64(math.Round(t0 + xBreach))
	trend.EstimatedAt = estimated
	if latest.Value >= cfg.Limit {
		trend.Breached = true
		trend.SecondsAhead = 0
		return trend
	}
	ahead := estimated - latest.At
	if ahead < 0 {
		ahead = 0
	}
	trend.SecondsAhead = ahead
	return trend
}

// CurrentTrend 拟合并返回当前开放趋势段的趋势。
func (b *TrendBook) CurrentTrend() (Trend, bool) {
	b.mu.Lock()
	cfg := b.cfg
	open := b.open
	b.mu.Unlock()
	if open == nil {
		return Trend{}, false
	}
	return Fit(*open, cfg), true
}

// Trends 拟合全部趋势段，最新段排在最后。
func (b *TrendBook) Trends() []Trend {
	cfg := b.config()
	segments := b.Segments()
	out := make([]Trend, 0, len(segments))
	for _, seg := range segments {
		out = append(out, Fit(seg, cfg))
	}
	return out
}

// EvaluateWarning 基于当前趋势段给出预警：
//   - 已越限：critical；
//   - 拟合可信（样本数达标）且预计在预警窗口内越界：warning；
//
// 其余情况不预警。预警附带该段全部支撑样本。
func (b *TrendBook) EvaluateWarning(now int64) (TrendWarning, bool) {
	cfg := b.config()
	seg, ok := b.CurrentSegment()
	if !ok || len(seg.Samples) == 0 {
		return TrendWarning{}, false
	}
	trend := Fit(seg, cfg)
	supporting := append([]model.Sample(nil), seg.Samples...)

	// 以当前时刻计算剩余时间；当前时刻早于最新样本时以最新样本为准。
	reference := now
	if latestAt := seg.Samples[len(seg.Samples)-1].At; latestAt > reference {
		reference = latestAt
	}

	switch {
	case trend.Breached:
		return TrendWarning{
			Trend:   trend,
			Level:   "critical",
			Message: "component value at or past the limit",
			Samples: supporting,
		}, true
	case trend.SampleCount >= cfg.MinSamples && trend.Slope > 0 && trend.EstimatedAt > 0 && trend.EstimatedAt-reference <= cfg.WarnHorizon:
		if trend.EstimatedAt < reference {
			// 外推已越界但最新样本尚未越限（数据滞后），仍按紧急处理。
			return TrendWarning{
				Trend:   trend,
				Level:   "critical",
				Message: "drift projection already past the limit",
				Samples: supporting,
			}, true
		}
		return TrendWarning{
			Trend:   trend,
			Level:   "warning",
			Message: "drift projects the limit within the warning horizon",
			Samples: supporting,
		}, true
	default:
		return TrendWarning{}, false
	}
}
