package analyzer

import (
	"github.com/google/uuid"
	"github.com/wyw14/solo-039/internal/model"
	"math"
	"testing"
)

func sampleAt(calibration, feed uuid.UUID, value float64, at int64) model.Sample {
	return model.Sample{ID: uuid.New(), FeedRevision: feed, Calibration: calibration, Component: "top", Value: value, At: at, Valid: true}
}

func TestTrendEstimatesSlopeAndCrossing(t *testing.T) {
	calibration := uuid.New()
	feed := uuid.New()
	trend := NewTrend(60)
	trend.Begin(calibration, feed, SegmentStart, 100)
	// 每 10 秒上升 0.01，即每秒 0.001。
	trend.Add(sampleAt(calibration, feed, 0.90, 100))
	trend.Add(sampleAt(calibration, feed, 0.91, 110))
	trend.Add(sampleAt(calibration, feed, 0.92, 120))

	limit := ComponentLimit{Component: "top", Max: 1.0, HasMax: true}
	report := trend.Report("top", limit, DefaultReportOptions())
	if len(report.Segments) != 1 {
		t.Fatalf("expected one segment, got %d", len(report.Segments))
	}
	if report.Current == nil {
		t.Fatal("expected current segment")
	}
	if math.Abs(report.Current.Slope-0.001) > 1e-9 {
		t.Fatalf("slope = %.6f, want 0.001", report.Current.Slope)
	}
	if len(report.Warnings) != 1 {
		t.Fatalf("expected one warning, got %d", len(report.Warnings))
	}
	warning := report.Warnings[0]
	if warning.Direction != "high" || warning.Limit != 1.0 {
		t.Fatalf("unexpected crossing %s@%.2f", warning.Direction, warning.Limit)
	}
	// 0.92 + 0.001*t = 1.0 -> 80 秒，越界时刻 200。
	if warning.CrossAt != 200 {
		t.Fatalf("cross at = %d, want 200", warning.CrossAt)
	}
	if len(warning.Support) != 3 {
		t.Fatalf("support samples = %d, want 3", len(warning.Support))
	}
}

func TestTrendSplitsOnMissingData(t *testing.T) {
	calibration := uuid.New()
	feed := uuid.New()
	trend := NewTrend(60)
	trend.Begin(calibration, feed, SegmentStart, 0)
	trend.Add(sampleAt(calibration, feed, 0.5, 0))
	trend.Add(sampleAt(calibration, feed, 0.51, 30))
	// 间隔 120 秒 > maxGap 60，视为缺测，必须另起一段。
	trend.Add(sampleAt(calibration, feed, 0.52, 150))

	segments := trend.Segments()
	if len(segments) != 2 {
		t.Fatalf("segments = %d, want 2", len(segments))
	}
	if !segments[0].Closed || segments[0].Reason != SegmentStart {
		t.Fatal("first segment should be closed by gap")
	}
	if segments[1].Reason != SegmentGap {
		t.Fatalf("second segment reason = %q, want gap", segments[1].Reason)
	}
}

func TestTrendSplitsOnRecalibration(t *testing.T) {
	first := uuid.New()
	second := uuid.New()
	feed := uuid.New()
	trend := NewTrend(60)
	trend.Begin(first, feed, SegmentStart, 0)
	trend.Add(sampleAt(first, feed, 0.5, 0))
	trend.Add(sampleAt(first, feed, 0.51, 10))
	trend.Begin(second, feed, SegmentCalibration, 20)
	trend.Add(sampleAt(second, feed, 0.5, 20))
	trend.Add(sampleAt(second, feed, 0.5, 30))

	segments := trend.Segments()
	if len(segments) != 2 {
		t.Fatalf("segments = %d, want 2", len(segments))
	}
	if segments[0].Calibration != first || !segments[0].Closed {
		t.Fatal("old calibration segment should be retained and closed")
	}
	if segments[1].Reason != SegmentCalibration || segments[1].Calibration != second {
		t.Fatal("recalibration must open a distinct segment")
	}

	// 新段只有两个点且平缓，旧段漂移不得串入预警。
	limit := ComponentLimit{Component: "top", Max: 0.55, HasMax: true}
	report := trend.Report("top", limit, DefaultReportOptions())
	if len(report.Warnings) != 0 {
		t.Fatalf("fresh calibration segment produced %d warnings; trends must not extrapolate across calibration", len(report.Warnings))
	}
}

func TestTrendNoProjectionWithSingleSample(t *testing.T) {
	calibration := uuid.New()
	feed := uuid.New()
	trend := NewTrend(60)
	trend.Begin(calibration, feed, SegmentStart, 0)
	trend.Add(sampleAt(calibration, feed, 0.99, 0))
	limit := ComponentLimit{Component: "top", Max: 1.0, HasMax: true}
	report := trend.Report("top", limit, DefaultReportOptions())
	if report.Current == nil || report.Current.Projectable {
		t.Fatal("single sample must not be projectable")
	}
	if len(report.Warnings) != 0 {
		t.Fatalf("single sample produced %d warnings", len(report.Warnings))
	}
}

func TestTrendRespectsHorizon(t *testing.T) {
	calibration := uuid.New()
	feed := uuid.New()
	trend := NewTrend(60)
	trend.Begin(calibration, feed, SegmentStart, 0)
	trend.Add(sampleAt(calibration, feed, 0.90, 0))
	trend.Add(sampleAt(calibration, feed, 0.91, 10))
	limit := ComponentLimit{Component: "top", Max: 1.0, HasMax: true}

	// 越界需 90 秒；30 秒预警窗内不应报警。
	report := trend.Report("top", limit, ReportOptions{MinSamples: 2, HorizonSeconds: 30})
	if len(report.Warnings) != 0 {
		t.Fatalf("horizon should filter crossing, got %d", len(report.Warnings))
	}
	report = trend.Report("top", limit, ReportOptions{MinSamples: 2, HorizonSeconds: 120})
	if len(report.Warnings) != 1 {
		t.Fatalf("expected crossing within 120s horizon, got %d", len(report.Warnings))
	}
}

func TestTrendLowerBound(t *testing.T) {
	calibration := uuid.New()
	feed := uuid.New()
	trend := NewTrend(60)
	trend.Begin(calibration, feed, SegmentStart, 0)
	trend.Add(sampleAt(calibration, feed, 0.10, 0))
	trend.Add(sampleAt(calibration, feed, 0.09, 10))
	limit := ComponentLimit{Component: "top", Min: 0.0, HasMin: true}
	report := trend.Report("top", limit, DefaultReportOptions())
	if len(report.Warnings) != 1 {
		t.Fatalf("expected one low-bound warning, got %d", len(report.Warnings))
	}
	if report.Warnings[0].Direction != "low" {
		t.Fatalf("direction = %q, want low", report.Warnings[0].Direction)
	}
}

func TestTrendSlopeWithUnixScaleTimestamps(t *testing.T) {
	// 真实采样时间戳是 1e9 量级；居中处理后斜率仍须精确。
	calibration := uuid.New()
	feed := uuid.New()
	trend := NewTrend(3600)
	base := int64(1_760_000_000)
	trend.Begin(calibration, feed, SegmentStart, base)
	trend.Add(sampleAt(calibration, feed, 0.90, base))
	trend.Add(sampleAt(calibration, feed, 0.91, base+60))
	trend.Add(sampleAt(calibration, feed, 0.92, base+120))

	report := trend.Report("top", ComponentLimit{Component: "top", Max: 1.0, HasMax: true}, DefaultReportOptions())
	if math.Abs(report.Current.Slope-0.01/60.0) > 1e-12 {
		t.Fatalf("slope = %.9f, want %.9f", report.Current.Slope, 0.01/60.0)
	}
	if len(report.Warnings) != 1 {
		t.Fatalf("warnings = %d, want 1", len(report.Warnings))
	}
	// 0.92 + (0.01/60)*t = 1.0 -> 480 秒。
	if report.Warnings[0].CrossAt != base+120+480 {
		t.Fatalf("cross at = %d, want %d", report.Warnings[0].CrossAt, base+600)
	}
}

func TestTrendSegmentsImmutableFromOutside(t *testing.T) {
	calibration := uuid.New()
	feed := uuid.New()
	trend := NewTrend(60)
	trend.Begin(calibration, feed, SegmentStart, 0)
	trend.Add(sampleAt(calibration, feed, 0.5, 0))
	segments := trend.Segments()
	segments[0].Samples[0].Value = 99
	again := trend.Segments()
	if again[0].Samples[0].Value == 99 {
		t.Fatal("Segments must return copies")
	}
}
