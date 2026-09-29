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
	trends      *TrendBook
	last        model.Proof
	online      bool
}

func New() *State {
	calibration := uuid.New()
	w := NewWindow()
	w.Reset(calibration, uuid.Nil)
	return &State{calibration: calibration, window: w, trends: NewTrendBook(DefaultTrendConfig()), online: true}
}

func (s *State) SetOnline(value bool) { s.mu.Lock(); s.online = value; s.mu.Unlock() }

func (s *State) Calibrate() uuid.UUID {
	s.mu.Lock()
	s.calibration = uuid.New()
	s.window.Reset(s.calibration, uuid.Nil)
	// 重新校准改变了数值基准，旧趋势不得与新样本连成一条线。
	s.trends.Split()
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
	// 切换进料修订改变了物料基准，趋势同样要切开。
	s.trends.Split()
	s.mu.Unlock()
}

func (s *State) Add(sample model.Sample) bool {
	s.mu.RLock()
	online := s.online
	w := s.window
	trends := s.trends
	s.mu.RUnlock()
	if !online {
		return false
	}
	if !w.Add(sample) {
		return false
	}
	// 趋势链是证明链之外的旁路记录：失败也不影响即时证明。
	_ = trends.Add(sample)
	return true
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

func (s *State) Trends() *TrendBook {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.trends
}

func (s *State) ConfigureTrend(cfg TrendConfig) {
	s.mu.RLock()
	trends := s.trends
	s.mu.RUnlock()
	trends.Configure(cfg)
}
