package api

import (
	"net/http"
	"time"

	"github.com/nextroad-dev/Auto-Router/internal/storage"
)

type dashboardMetric struct {
	Numerator   int64    `json:"numerator"`
	Denominator int64    `json:"denominator"`
	Value       *float64 `json:"value"`
}

type dashboardCountRow struct {
	ID           string `json:"id"`
	Name         string `json:"name,omitempty"`
	Requests     int64  `json:"requests"`
	InputTokens  *int64 `json:"input_tokens"`
	OutputTokens *int64 `json:"output_tokens"`
	TotalTokens  *int64 `json:"total_tokens"`
}

// handleDashboardReport is intentionally narrower than the diagnostic summary:
// one final client success rate, actual upstream model/group attempts and their
// observed token sums, plus 60-second completed-output TPS.
func (h *adminHandler) handleDashboardReport(w http.ResponseWriter, r *http.Request) {
	if !h.requireDatabase(w) {
		return
	}
	windowKey, window, err := summaryWindow(r.URL.Query().Get("window"))
	if err != nil {
		writeAdminError(w, http.StatusBadRequest, "invalid_filter", err.Error(), "window")
		return
	}
	now := time.Now().UTC()
	from := now.Add(-window)
	store := storage.NewRoutingLog(h.db)
	overall, err := store.Stats(r.Context(), storage.StatsQuery{Filter: storage.EventFilter{From: from, To: now}})
	if err != nil {
		storageFailure(w, h, "aggregate dashboard requests", err)
		return
	}
	metric := dashboardMetric{
		Numerator:   sumField(overall, func(row storage.EventStats) int64 { return row.Success }),
		Denominator: sumField(overall, func(row storage.EventStats) int64 { return row.Count }),
	}
	if metric.Denominator > 0 {
		value := float64(metric.Numerator) / float64(metric.Denominator)
		metric.Value = &value
	}
	modelStats, err := store.AttemptStats(r.Context(), storage.AttemptStatsQuery{From: from, To: now, GroupBy: "model"})
	if err != nil {
		storageFailure(w, h, "aggregate dashboard model attempts", err)
		return
	}
	groupStats, err := store.AttemptStats(r.Context(), storage.AttemptStatsQuery{From: from, To: now, GroupBy: "group"})
	if err != nil {
		storageFailure(w, h, "aggregate dashboard group attempts", err)
		return
	}
	modelRows, err := h.dashboardModels(r, modelStats)
	if err != nil {
		storageFailure(w, h, "list dashboard models", err)
		return
	}
	groups := []dashboardCountRow{
		{ID: "simple", Name: "简单"},
		{ID: "medium", Name: "中等"},
		{ID: "complex", Name: "复杂"},
	}
	for _, stat := range groupStats {
		for i := range groups {
			if groups[i].ID == stat.Group {
				groups[i].Requests = stat.Count
				groups[i].InputTokens = stat.InputTokens
				groups[i].OutputTokens = stat.OutputTokens
				groups[i].TotalTokens = stat.TotalTokens
			}
		}
	}
	tps, err := store.OutputTokensPerSecond(r.Context(), now)
	if err != nil {
		storageFailure(w, h, "aggregate dashboard output TPS", err)
		return
	}
	writeAdminJSON(w, http.StatusOK, struct {
		Window       summaryWindowPayload `json:"window"`
		SuccessRate  dashboardMetric      `json:"success_rate"`
		Models       []dashboardCountRow  `json:"models"`
		Groups       []dashboardCountRow  `json:"groups"`
		OutputTPS60s *float64             `json:"output_tps_60s"`
	}{
		Window:      summaryWindowPayload{Key: windowKey, From: from.Format(time.RFC3339Nano), To: now.Format(time.RFC3339Nano)},
		SuccessRate: metric, Models: modelRows, Groups: groups, OutputTPS60s: tps,
	})
}

func (h *adminHandler) dashboardModels(r *http.Request, stats []storage.AttemptStats) ([]dashboardCountRow, error) {
	rows, err := h.db.QueryContext(r.Context(), `
		SELECT m.id, m.display_name FROM models m
		WHERE m.enabled=1 AND EXISTS (
			SELECT 1 FROM provider_models pm JOIN providers p ON p.key=pm.provider_key
			WHERE pm.model_id=m.id AND pm.enabled=1 AND p.enabled=1
		)
		ORDER BY m.priority, m.id LIMIT 200
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]dashboardCountRow, 0, 32)
	index := map[string]int{}
	for rows.Next() {
		var row dashboardCountRow
		if err := rows.Scan(&row.ID, &row.Name); err != nil {
			return nil, err
		}
		index[row.ID] = len(result)
		result = append(result, row)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, stat := range stats {
		if stat.Group == "" {
			continue
		}
		i, found := index[stat.Group]
		if !found {
			i = len(result)
			index[stat.Group] = i
			result = append(result, dashboardCountRow{ID: stat.Group})
		}
		result[i].Requests = stat.Count
		result[i].InputTokens = stat.InputTokens
		result[i].OutputTokens = stat.OutputTokens
		result[i].TotalTokens = stat.TotalTokens
	}
	return result, nil
}
