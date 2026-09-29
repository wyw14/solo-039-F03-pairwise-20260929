package analyzer

import (
	"github.com/google/uuid"
	"github.com/wyw14/solo-039/internal/model"
	"sync"
)

type State struct {
	mu          sync.RWMutex
	calibration uuid.UUID
	window      *Window
	last        model.Proof
	online      bool
	component   string
	limit       ComponentLimit
	report      ReportOptions
}

func New() *State {
	calibration := uuid.New()
	w := NewWindow()
	w.Reset(calibration, uuid.Nil)
	return &State{
		calibration: calibration,
		window:      w,
		online:      true,
		component:   "top",
		limit:       ComponentLimit{Component: "top", Max: 1, HasMax: true},
		report:      DefaultReportOptions(),
	}
}

func (s *State) SetOnline(value bool) { s.mu.Lock(); s.online = value; s.mu.Unlock() }

func (s *State) Calibrate() uuid.UUID {
	s.mu.Lock()
	s.calibration = uuid.New()
	s.window.Reset(s.calibration, uuid.Nil)
	s.mu.Unlock()
	return s.calibration
}

func (s *State) Calibration() uuid.UUID {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.calibration
}

func (s *State) BindFeed(revision uuid.UUID) {
	s.mu.Lock()
	s.window.Reset(s.calibration, revision)
	s.mu.Unlock()
}

func (s *State) Add(sample model.Sample) bool {
	s.mu.RLock()
	online := s.online
	w := s.window
	s.mu.RUnlock()
	if !online {
		return false
	}
	return w.Add(sample)
}

func (s *State) Proof(revision uuid.UUID) (model.Proof, bool) {
	s.mu.RLock()
	online := s.online
	complete := s.window.Complete()
	calibration := s.calibration
	s.mu.RUnlock()
	if !online || !complete {
		return model.Proof{}, false
	}
	proof := model.Proof{ID: uuid.New(), Revision: revision, Source: calibration.String(), At: 1, Independent: true}
	proof.Digest = model.ProofDigest(proof)
	s.mu.Lock()
	s.last = proof
	s.mu.Unlock()
	return proof, true
}

func (s *State) LastProof() model.Proof { s.mu.RLock(); defer s.mu.RUnlock(); return s.last }

func (s *State) ConfigureWindow(needed int, maxGap int64) {
	s.mu.RLock()
	window := s.window
	s.mu.RUnlock()
	window.Configure(needed, maxGap)
}

func (s *State) SampleCount() int {
	s.mu.RLock()
	window := s.window
	s.mu.RUnlock()
	return len(window.Samples())
}

// ConfigureLimit 设置组分合格区间；min/max 传 NaN 或 +Inf 可用 has 标志按需启用。
func (s *State) ConfigureLimit(component string, min, max float64, hasMin, hasMax bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if component != "" {
		s.component = component
	}
	s.limit = ComponentLimit{Component: s.component, Min: min, Max: max, HasMin: hasMin, HasMax: hasMax}
}

// ConfigureTrend 设置漂移外推参数（最少样本数、预警时间窗秒）。
func (s *State) ConfigureTrend(minSamples int, horizonSeconds int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if minSamples > 0 {
		s.report.MinSamples = minSamples
	}
	s.report.HorizonSeconds = horizonSeconds
}

// TrendReport 返回分段时间趋势、各段漂移速度与当前段越界预警（含支撑样本）。
func (s *State) TrendReport() Report {
	s.mu.RLock()
	window := s.window
	component := s.component
	limit := s.limit
	opts := s.report
	s.mu.RUnlock()
	return window.Trend().Report(component, limit, opts)
}

// TrendQuery 是时间趋势的查询参数；Limit 为 nil 时用已配置的组分限值。
type TrendQuery struct {
	Limit   *ComponentLimit
	Options ReportOptions
}

// QueryTrend 按查询参数返回趋势；nil 字段回退到状态的默认配置。
func (s *State) QueryTrend(query TrendQuery) Report {
	s.mu.RLock()
	window := s.window
	component := s.component
	limit := s.limit
	opts := s.report
	s.mu.RUnlock()
	if query.Limit != nil {
		limit = *query.Limit
		if limit.Component != "" {
			component = limit.Component
		}
	}
	if query.Options.MinSamples > 0 {
		opts.MinSamples = query.Options.MinSamples
	}
	if query.Options.HorizonSeconds > 0 {
		opts.HorizonSeconds = query.Options.HorizonSeconds
	}
	return window.Trend().Report(component, limit, opts)
}

// TrendReportWith 在默认外推参数上叠加临时覆盖（如按查询限定预警时间窗）。
func (s *State) TrendReportWith(overrides ReportOptions) Report {
	return s.QueryTrend(TrendQuery{Options: overrides})
}

// Warnings 仅返回当前段的漂移越界预警。
func (s *State) Warnings() []Warning {
	return s.TrendReport().Warnings
}

// TrendSegments 返回全部趋势段（含已关闭段），供查询与审计。
func (s *State) TrendSegments() []Segment {
	s.mu.RLock()
	window := s.window
	s.mu.RUnlock()
	return window.Trend().Segments()
}
