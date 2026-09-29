package analyzer

import (
	"github.com/google/uuid"
	"github.com/wyw14/solo-039/internal/model"
	"testing"
)

func trendSample(calibration, feed uuid.UUID, at int64, value float64) model.Sample {
	return model.Sample{ID: uuid.New(), Calibration: calibration, FeedRevision: feed, At: at, Value: value, Component: "top", Valid: true}
}

func TestTrendDriftProjectsBreach(t *testing.T) {
	book := NewTrendBook(TrendConfig{MaxGap: 60, MinSamples: 2, Limit: 1.0, WarnHorizon: 600, MaxSegments: 10})
	cal, feed := uuid.New(), uuid.New()
	// 每 50 秒上涨 0.05（0.001/秒）：0.8 @1000 → 0.85 @1050，预计在 1200 达到 1.0。
	for _, v := range []struct {
		at  int64
		val float64
	}{{1000, 0.8}, {1050, 0.85}} {
		if !book.Add(trendSample(cal, feed, v.at, v.val)) {
			t.Fatal("sample rejected")
		}
	}
	trend, ok := book.CurrentTrend()
	if !ok {
		t.Fatal("missing current trend")
	}
	if trend.Slope <= 0 {
		t.Fatalf("expected positive drift, got %v", trend.Slope)
	}
	if trend.EstimatedAt != 1200 {
		t.Fatalf("expected breach at 1200, got %d", trend.EstimatedAt)
	}
	if trend.Breached {
		t.Fatal("latest sample is below the limit")
	}
	warning, warned := book.EvaluateWarning(1100)
	if !warned || warning.Level != "warning" {
		t.Fatalf("expected warning, got %+v warned=%v", warning, warned)
	}
	if len(warning.Samples) != 2 {
		t.Fatalf("warning must list supporting samples, got %d", len(warning.Samples))
	}
	if warning.Trend.SegmentID != trend.SegmentID {
		t.Fatal("warning trend must reference its segment")
	}
}

func TestTrendNoWarningWhenStableOrFarOut(t *testing.T) {
	book := NewTrendBook(DefaultTrendConfig())
	cal, feed := uuid.New(), uuid.New()
	// 缓慢下降，不逼近限值。
	book.Add(trendSample(cal, feed, 1000, 0.5))
	book.Add(trendSample(cal, feed, 1050, 0.49))
	if _, warned := book.EvaluateWarning(1050); warned {
		t.Fatal("declining values must not warn")
	}

	book2 := NewTrendBook(DefaultTrendConfig())
	// 正漂移但预计越界在很久以后（每 50s 涨 0.001）。
	book2.Add(trendSample(cal, feed, 1000, 0.5))
	book2.Add(trendSample(cal, feed, 1050, 0.501))
	if _, warned := book2.EvaluateWarning(1050); warned {
		t.Fatal("breach outside the horizon must not warn")
	}
}

func TestTrendCriticalWhenBreached(t *testing.T) {
	book := NewTrendBook(DefaultTrendConfig())
	cal, feed := uuid.New(), uuid.New()
	book.Add(trendSample(cal, feed, 1000, 0.99))
	book.Add(trendSample(cal, feed, 1050, 1.05))
	warning, warned := book.EvaluateWarning(1050)
	if !warned || warning.Level != "critical" {
		t.Fatalf("expected critical breach warning, got %+v", warning)
	}
}

func TestTrendGapOpensNewSegment(t *testing.T) {
	book := NewTrendBook(TrendConfig{MaxGap: 60, MinSamples: 2, Limit: 1.0, WarnHorizon: 600, MaxSegments: 10})
	cal, feed := uuid.New(), uuid.New()
	book.Add(trendSample(cal, feed, 1000, 0.5))
	book.Add(trendSample(cal, feed, 1030, 0.51))
	// 缺测超过 60s：必须切开。
	book.Add(trendSample(cal, feed, 1200, 0.52))
	segments := book.Segments()
	if len(segments) != 2 {
		t.Fatalf("expected gap to split trend into 2 segments, got %d", len(segments))
	}
	if !segments[0].Closed || segments[1].Closed {
		t.Fatal("first segment must be closed and second open")
	}
	if len(segments[0].Samples) != 2 || len(segments[1].Samples) != 1 {
		t.Fatal("samples must stay inside their own segment")
	}
}

func TestTrendSplitOnCalibrateAndFeed(t *testing.T) {
	state := New()
	cal := state.Calibration()
	feedA := uuid.New()
	state.BindFeed(feedA)
	state.Add(trendSample(cal, feedA, 1000, 0.5))
	state.Add(trendSample(cal, feedA, 1010, 0.51))

	// 切换进料修订切段。
	feedB := uuid.New()
	state.BindFeed(feedB)
	newCal := state.Calibration()
	state.Add(trendSample(newCal, feedB, 1100, 0.6))

	// 重新校准切段，且旧校准样本被拒绝。
	cal2 := state.Calibrate()
	state.BindFeed(feedB)
	if state.Add(trendSample(newCal, feedB, 1200, 0.61)) {
		t.Fatal("sample from previous calibration must be rejected")
	}
	state.Add(trendSample(cal2, feedB, 1210, 0.62))

	segments := state.Trends().Segments()
	if len(segments) != 3 {
		t.Fatalf("expected 3 segments after feed switch and recalibration, got %d", len(segments))
	}
}

func TestTrendProofFlowUnaffected(t *testing.T) {
	state := New()
	feed := uuid.New()
	cal := state.Calibration()
	state.BindFeed(feed)
	state.ConfigureWindow(3, 60)
	for _, at := range []int64{1000, 1010, 1020} {
		if !state.Add(trendSample(cal, feed, at, 0.5)) {
			t.Fatal("stable sample rejected")
		}
	}
	if _, ok := state.Proof(feed); !ok {
		t.Fatal("immediate proof must still be produced from the window")
	}
	trends := state.Trends().Trends()
	if len(trends) != 1 || trends[0].SampleCount != 3 {
		t.Fatalf("trend chain must record the same accepted samples, got %+v", trends)
	}
}

func TestTrendRecoveryPreservesSegments(t *testing.T) {
	state := New()
	cal, feed := state.Calibration(), uuid.New()
	state.BindFeed(feed)
	state.Add(trendSample(cal, feed, 1000, 0.5))
	state.Add(trendSample(cal, feed, 1030, 0.6))
	state.Add(trendSample(cal, feed, 1300, 0.7)) // 缺测切段

	recovery := Capture(state)
	if len(recovery.Segments) != 2 {
		t.Fatalf("expected 2 captured segments, got %d", len(recovery.Segments))
	}
	clone := New()
	Restore(clone, recovery)
	segments := clone.Trends().Segments()
	if len(segments) != 2 {
		t.Fatalf("expected 2 restored segments, got %d", len(segments))
	}
	if clone.SampleCount() != 1 {
		t.Fatalf("window keeps only the current run, got %d samples", clone.SampleCount())
	}
	// 重放同一时刻样本不得在趋势段里产生重复；30s 间隔仍属同一段。
	clone.Add(trendSample(cal, feed, 1330, 0.71))
	segments = clone.Trends().Segments()
	open := segments[len(segments)-1]
	if len(open.Samples) != 2 {
		t.Fatalf("open segment should hold 1 restored + 1 new sample, got %d", len(open.Samples))
	}
}
