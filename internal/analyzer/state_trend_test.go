package analyzer

import (
	"github.com/google/uuid"
	"github.com/wyw14/solo-039/internal/model"
	"testing"
)

func addAt(t *testing.T, state *State, calibration, feed uuid.UUID, value float64, at int64) {
	t.Helper()
	if !state.Add(model.Sample{ID: uuid.New(), FeedRevision: feed, Calibration: calibration, Component: "top", Value: value, At: at, Valid: true}) {
		t.Fatalf("sample at %d rejected", at)
	}
}

func TestStateDriftWarningAcrossProofFlow(t *testing.T) {
	state := New()
	state.ConfigureLimit("top", 0, 1, false, true)
	feed := uuid.New()
	state.BindFeed(feed)
	calibration := state.Calibration()

	// 缓慢逼近上限 1.0：每秒 +0.001，证明窗口同时可用。
	addAt(t, state, calibration, feed, 0.90, 100)
	addAt(t, state, calibration, feed, 0.91, 110)
	addAt(t, state, calibration, feed, 0.92, 120)

	warnings := state.Warnings()
	if len(warnings) != 1 {
		t.Fatalf("warnings = %d, want 1", len(warnings))
	}
	if warnings[0].CrossAt != 200 {
		t.Fatalf("cross at = %d, want 200", warnings[0].CrossAt)
	}
	if len(warnings[0].Support) != 3 {
		t.Fatalf("support = %d, want 3", len(warnings[0].Support))
	}

	// 即时证明流程不受影响：三样本稳定且完整，仍可出证。
	proof, ok := state.Proof(feed)
	if !ok {
		t.Fatal("proof must still be available alongside drift warning")
	}
	if proof.Source != calibration.String() {
		t.Fatal("proof source should bind to current calibration")
	}
}

func TestStateCalibrationCutsTrendSegment(t *testing.T) {
	state := New()
	state.ConfigureLimit("top", 0, 1, false, true)
	feed := uuid.New()
	state.BindFeed(feed)
	first := state.Calibration()

	addAt(t, state, first, feed, 0.9, 0)
	addAt(t, state, first, feed, 0.91, 10)

	second := state.Calibrate()
	state.BindFeed(feed)
	addAt(t, state, second, feed, 0.5, 20)
	addAt(t, state, second, feed, 0.5, 30)

	report := state.TrendReport()
	if len(report.Segments) != 2 {
		t.Fatalf("segments = %d, want 2", len(report.Segments))
	}
	if len(report.Warnings) != 0 {
		t.Fatalf("recalibrated trend must not carry the old drift, got %d warnings", len(report.Warnings))
	}
}

func TestStateGapCutsTrendSegment(t *testing.T) {
	state := New()
	state.ConfigureLimit("top", 0, 1, false, true)
	state.ConfigureWindow(2, 60)
	feed := uuid.New()
	state.BindFeed(feed)
	calibration := state.Calibration()

	addAt(t, state, calibration, feed, 0.9, 0)
	addAt(t, state, calibration, feed, 0.91, 10)
	// 缺测 300 秒，切开趋势段与证明窗口。
	addAt(t, state, calibration, feed, 0.99, 310)

	report := state.TrendReport()
	if len(report.Segments) != 2 {
		t.Fatalf("segments = %d, want 2 after gap", len(report.Segments))
	}
	if report.Current == nil || report.Current.Reason != string(SegmentGap) {
		t.Fatal("current segment must be marked as gap-started")
	}
	if state.SampleCount() != 1 {
		t.Fatalf("proof window samples = %d, want 1 after gap", state.SampleCount())
	}
}

func TestRecoveryRetainsTrendSegments(t *testing.T) {
	state := New()
	state.ConfigureLimit("top", 0, 1, false, true)
	feed := uuid.New()
	state.BindFeed(feed)
	calibration := state.Calibration()
	addAt(t, state, calibration, feed, 0.9, 0)
	addAt(t, state, calibration, feed, 0.91, 10)
	second := state.Calibrate()
	state.BindFeed(feed)
	addAt(t, state, second, feed, 0.5, 20)

	recovery := Capture(state)
	if len(recovery.Segments) != 2 {
		t.Fatalf("captured segments = %d, want 2", len(recovery.Segments))
	}

	restored := New()
	Restore(restored, recovery)
	report := restored.TrendReport()
	if len(report.Segments) != 2 {
		t.Fatalf("restored segments = %d, want 2", len(report.Segments))
	}
	if got := restored.Calibration(); got != second {
		t.Fatalf("restored calibration = %v, want %v", got, second)
	}
}
