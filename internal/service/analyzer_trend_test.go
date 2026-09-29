package service

import (
	"github.com/google/uuid"
	"github.com/wyw14/solo-039/internal/analyzer"
	"github.com/wyw14/solo-039/internal/model"
	"path/filepath"
	"testing"
)

func TestPlantAnalyzerTrends(t *testing.T) {
	plant := NewPlant(filepath.Join(t.TempDir(), "data"))
	plant.ConfigureAnalyzerLimit("top", 0, 1, false, true)
	feed := uuid.New()
	plant.Analyzer.BindFeed(feed)
	calibration := plant.Analyzer.Calibration()

	add := func(value float64, at int64) {
		t.Helper()
		if !plant.Analyzer.Add(model.Sample{ID: uuid.New(), FeedRevision: feed, Calibration: calibration, Component: "top", Value: value, At: at, Valid: true}) {
			t.Fatalf("sample at %d rejected", at)
		}
	}
	add(0.90, 100)
	add(0.91, 110)
	add(0.92, 120)

	report := plant.AnalyzerTrends(AnalyzerTrendQuery{})
	if len(report.Segments) != 1 || len(report.Warnings) != 1 {
		t.Fatalf("segments=%d warnings=%d, want 1/1", len(report.Segments), len(report.Warnings))
	}
	if got := report.Warnings[0].CrossAt; got != 200 {
		t.Fatalf("cross at = %d, want 200", got)
	}

	// 查询级时间窗覆盖应过滤掉 80 秒后的越界。
	filtered := plant.AnalyzerTrends(AnalyzerTrendQuery{HorizonSeconds: 30})
	if len(filtered.Warnings) != 0 {
		t.Fatalf("horizon filter left %d warnings", len(filtered.Warnings))
	}

	// 查询级限值覆盖生效：更紧的上限应更早越界。
	tighter := plant.AnalyzerTrends(AnalyzerTrendQuery{Component: "top", Max: 0.95, HasMax: true, HasLimit: true})
	if len(tighter.Warnings) != 1 || tighter.Warnings[0].Limit != 0.95 {
		t.Fatalf("query limit override not applied: %+v", tighter.Warnings)
	}
}

func TestPlantStatusExposesDriftWarnings(t *testing.T) {
	plant := NewPlant(filepath.Join(t.TempDir(), "data"))
	plant.Analyzer.ConfigureLimit("top", 0, 1, false, true)
	feed := uuid.New()
	plant.Analyzer.BindFeed(feed)
	calibration := plant.Analyzer.Calibration()
	for _, value := range []float64{0.9, 0.91, 0.92} {
		var at int64 = 100 + int64(value*1000)
		_ = plant.Analyzer.Add(model.Sample{ID: uuid.New(), FeedRevision: feed, Calibration: calibration, Component: "top", Value: value, At: at, Valid: true})
	}
	status := plant.Status()
	warnings, ok := status["analyzer_drift_warnings"].([]analyzer.Warning)
	if !ok {
		t.Fatal("status missing analyzer_drift_warnings")
	}
	if len(warnings) != 1 {
		t.Fatalf("status warnings = %d, want 1", len(warnings))
	}
	if status["analyzer_trend_segments"].(int) != 1 {
		t.Fatal("status segment count mismatch")
	}
}
