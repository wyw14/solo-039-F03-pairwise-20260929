package api

import (
	"github.com/wyw14/solo-039/internal/service"
	"net/http"
	"strconv"
)

// trends 返回采样链的分段时间趋势、漂移速度、预计越界时刻及预警支撑样本。
// 查询参数：
//
//	horizon     仅保留该秒数内会越界的预警（可选）
//	min_samples 可外推斜率的最少样本数（可选，默认 2）
//	max / min   临时覆盖组分合格上下限（二者至少给一个才生效）
func (s *Server) trends(w http.ResponseWriter, r *http.Request) {
	query := service.AnalyzerTrendQuery{}
	if raw := r.URL.Query().Get("horizon"); raw != "" {
		value, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || value < 0 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid horizon"})
			return
		}
		query.HorizonSeconds = value
	}
	if raw := r.URL.Query().Get("min_samples"); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value <= 0 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid min_samples"})
			return
		}
		query.MinSamples = value
	}
	if rawMax, hasMax := r.URL.Query()["max"]; hasMax {
		value, err := strconv.ParseFloat(rawMax[0], 64)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid max"})
			return
		}
		query.Max = value
		query.HasMax = true
		query.HasLimit = true
	}
	if rawMin, hasMin := r.URL.Query()["min"]; hasMin {
		value, err := strconv.ParseFloat(rawMin[0], 64)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid min"})
			return
		}
		query.Min = value
		query.HasMin = true
		query.HasLimit = true
	}
	query.Component = "top"
	revision := s.plant.Campaign.Snapshot().Revision.String()
	report := s.plant.AnalyzerTrends(query)
	writeJSON(w, http.StatusOK, map[string]any{"revision": revision, "report": report})
}
