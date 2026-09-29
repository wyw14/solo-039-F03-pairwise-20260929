package analyzer

import (
	"github.com/google/uuid"
	"github.com/wyw14/solo-039/internal/model"
	"testing"
)

func TestWindowRejectsGenerationMismatch(t *testing.T) {
	window := NewWindow()
	calibration := uuid.New()
	feedRevision := uuid.New()
	window.Reset(calibration, feedRevision)
	if window.Add(model.Sample{Calibration: uuid.New(), FeedRevision: feedRevision, Valid: true, At: 1}) {
		t.Fatal("accepted another calibration")
	}
}

func TestWindowProofViewIsCurrentSegmentOnly(t *testing.T) {
	window := NewWindow()
	window.Configure(3, 60)
	calibration := uuid.New()
	feedRevision := uuid.New()
	window.Reset(calibration, feedRevision)

	add := func(at int64, value float64) {
		t.Helper()
		if !window.Add(model.Sample{ID: uuid.New(), Calibration: calibration, FeedRevision: feedRevision, Valid: true, At: at, Value: value}) {
			t.Fatalf("sample at %d rejected", at)
		}
	}
	add(0, 0.5)
	add(10, 0.51)
	add(20, 0.52)
	if !window.Complete() {
		t.Fatal("window should be complete with three current-segment samples")
	}
	// 缺测后另起一段：证明窗口必须清空重来，但历史段仍保留。
	add(200, 0.53)
	if window.Complete() {
		t.Fatal("gap must reset the proof window to the fresh segment")
	}
	if len(window.Samples()) != 1 {
		t.Fatalf("current segment samples = %d, want 1", len(window.Samples()))
	}
	segments := window.Trend().Segments()
	if len(segments) != 2 || len(segments[0].Samples) != 3 {
		t.Fatalf("trend history = %d segments, want prior 3-sample segment retained", len(segments))
	}
}

func TestWindowResetClosesTrendSegment(t *testing.T) {
	window := NewWindow()
	first := uuid.New()
	second := uuid.New()
	feed := uuid.New()
	window.Reset(first, feed)
	if !window.Add(model.Sample{ID: uuid.New(), Calibration: first, FeedRevision: feed, Valid: true, At: 1, Value: 0.5}) {
		t.Fatal("first generation sample rejected")
	}
	window.Reset(second, feed)
	if window.Add(model.Sample{ID: uuid.New(), Calibration: first, FeedRevision: feed, Valid: true, At: 2, Value: 0.5}) {
		t.Fatal("sample from recalibrated generation must be rejected")
	}
	if !window.Add(model.Sample{ID: uuid.New(), Calibration: second, FeedRevision: feed, Valid: true, At: 2, Value: 0.5}) {
		t.Fatal("new calibration sample rejected")
	}
	segments := window.Trend().Segments()
	if len(segments) != 2 {
		t.Fatalf("segments = %d, want 2 after recalibration", len(segments))
	}
}
