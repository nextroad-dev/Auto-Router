package api

import (
	"net/http"
	"time"

	"github.com/nextroad-dev/Auto-Router/internal/logging"
	"github.com/nextroad-dev/Auto-Router/internal/storage"
)

// This file implements GET /admin/v1/logs/summary: the one endpoint that owns the
// dashboard's metric definitions.
//
// The endpoint exists because the alternative is worse. A page that assembled its
// own numbers from /logs and /logs/stats would define each rate in JavaScript, which
// means the definition would live in the one place no test reads. Here every ratio
// is computed once, from the log's own columns, and the page renders what it is
// given.
//
// Three decisions are baked in, and each of them is a refusal to report a number
// that would be misleading:
//
//   - There is no single "hit rate". No ground truth exists for the correct model,
//     so a number called "Auto Router hit rate" would be read as a routing-accuracy
//     claim that nothing in this system can measure. Four measured ratios are
//     reported instead, each as a numerator and a denominator.
//   - A rate whose denominator is zero has a null value, never 0. "No automatic
//     request was made in this window" and "every automatic request was adopted" are
//     different findings, and reporting the first as 0% would turn an empty window
//     into a failure.
//   - Token sums keep the stage 8 semantics: a sum over zero observed rows is null,
//     and the observation counts travel with it.

// summaryWindows is the closed set of reporting windows. It is a closed set because
// an arbitrary duration would let a caller ask for a window the indexes were not
// designed for, and because a fixed set is what a UI can offer as a dropdown.
var summaryWindows = map[string]time.Duration{
	"1h":  time.Hour,
	"24h": 24 * time.Hour,
	"7d":  7 * 24 * time.Hour,
	"30d": 30 * 24 * time.Hour,
}

// defaultSummaryWindow is the window a request that asks for nothing gets.
const defaultSummaryWindow = "24h"

// summaryWindowValues returns the accepted windows in a fixed order.
func summaryWindowValues() []string { return []string{"1h", "24h", "7d", "30d"} }

// summaryWindow resolves one window parameter. An unknown value is refused rather
// than clamped: a caller who asked for "2h" would otherwise silently receive 24h.
func summaryWindow(raw string) (string, time.Duration, error) {
	if raw == "" {
		raw = defaultSummaryWindow
	}
	duration, ok := summaryWindows[raw]
	if !ok {
		return "", 0, errInvalidWindow
	}
	return raw, duration, nil
}

type summaryError string

func (e summaryError) Error() string { return string(e) }

var errInvalidWindow = summaryError("the window is not one of the documented values")

// errAdoptionInconsistent reports a numerator larger than its denominator. It is a
// programming error rather than a client error, and it is reported rather than
// published because a rate above 100% would be read as a real measurement.
var errAdoptionInconsistent = summaryError("the adoption counts are inconsistent")

// ratePayload is one measured ratio. The numerator and the denominator are always
// present, and the labels name both sides of the division, so a reader cannot
// mistake the number for an accuracy score.
type ratePayload struct {
	Name             string   `json:"name"`
	Numerator        int64    `json:"numerator"`
	Denominator      int64    `json:"denominator"`
	Value            *float64 `json:"value"`
	NumeratorLabel   string   `json:"numerator_label"`
	DenominatorLabel string   `json:"denominator_label"`
}

// ratio renders one rate. A zero denominator yields a null value, which is the
// documented "no data" answer.
func ratio(name string, numerator, denominator int64, numeratorLabel, denominatorLabel string) ratePayload {
	payload := ratePayload{
		Name:             name,
		Numerator:        numerator,
		Denominator:      denominator,
		NumeratorLabel:   numeratorLabel,
		DenominatorLabel: denominatorLabel,
	}
	if denominator > 0 {
		value := float64(numerator) / float64(denominator)
		payload.Value = &value
	}
	return payload
}

// summaryLatency is one latency class.
type summaryLatency struct {
	Count          int64    `json:"count"`
	MeanDurationMS *float64 `json:"mean_duration_ms"`
	P95DurationMS  *int64   `json:"p95_duration_ms"`
}

// summaryTokens is the token aggregate. Every sum is null when no row reported that
// count, and the observation counts say how many rows the sums cover.
type summaryTokens struct {
	InputTokens         *int64 `json:"input_tokens"`
	OutputTokens        *int64 `json:"output_tokens"`
	TotalTokens         *int64 `json:"total_tokens"`
	ObservedUsageCount  int64  `json:"observed_usage_count"`
	InputObservedCount  int64  `json:"input_observed_count"`
	OutputObservedCount int64  `json:"output_observed_count"`
	TotalObservedCount  int64  `json:"total_observed_count"`
}

// summaryStatuses is the status-class distribution.
type summaryStatuses struct {
	Success     int64 `json:"success"`
	ClientError int64 `json:"client_error"`
	ServerError int64 `json:"server_error"`
}

// summaryModel is one scoreboard row.
type summaryModel struct {
	Model           string `json:"model"`
	Decisions       int64  `json:"decisions"`
	AdoptedTop1     int64  `json:"adopted_top1"`
	RecommendedTop1 int64  `json:"recommended_top1"`
}

// summaryWindowPayload names the window the numbers were computed over, so a
// response is self-describing and two responses can be compared by their bounds
// rather than by the key they were requested with.
type summaryWindowPayload struct {
	Key  string `json:"key"`
	From string `json:"from"`
	To   string `json:"to"`
}

// summaryRequests is the request distribution.
type summaryRequests struct {
	Total    int64 `json:"total"`
	Auto     int64 `json:"auto"`
	Explicit int64 `json:"explicit"`
}

// summaryPayload is the whole report.
type summaryPayload struct {
	Window   summaryWindowPayload `json:"window"`
	Requests summaryRequests      `json:"requests"`
	Rates    []ratePayload        `json:"rates"`
	Latency  struct {
		All  summaryLatency `json:"all"`
		Auto summaryLatency `json:"auto"`
	} `json:"latency"`
	Tokens                       summaryTokens   `json:"tokens"`
	AttemptOutputTokensPerSecond *float64        `json:"attempt_output_tokens_per_second_60s"`
	Statuses                     summaryStatuses `json:"statuses"`
	Models                       []summaryModel  `json:"models"`
	// Recent is the newest events, so the dashboard page needs one request rather
	// than two. It is the same shape the list endpoint returns.
	Recent []logEventPayload `json:"recent"`
	// LogDisabled reports whether the durable routing log is currently off. A page
	// that shows empty numbers because the log is off must say so rather than leave
	// an operator to guess.
	LogDisabled bool `json:"log_disabled"`
}

// maxSummaryModels bounds the scoreboard the summary returns. It is the same bound
// the aggregate query uses, so a window with many models cannot make one request
// allocate without limit.
const maxSummaryModels = 50

// summaryRecentLimit is how many recent events the summary carries.
const summaryRecentLimit = 10

// handleLogSummary serves GET /admin/v1/logs/summary.
//
// The numbers come from the storage layer's existing aggregate:
//
//   - three Stats calls supply the routing-mode, selection-mode and Jev-status
//     distributions plus the latency and token aggregates, so the summary cannot
//     define a rate differently from /logs/stats;
//   - one ModelScoreboard call supplies the adoption counts;
//   - one event query supplies the recent rows and, with the mode totals, the
//     request distribution.
//
// Every count a rate divides is read from a stored column of a row that is inside
// the same window, so the numerator is always a subset of its denominator.
func (h *adminHandler) handleLogSummary(w http.ResponseWriter, r *http.Request) {
	if !h.requireDatabase(w) {
		return
	}
	key, window, err := summaryWindow(r.URL.Query().Get("window"))
	if err != nil {
		writeAdminError(w, http.StatusBadRequest, "invalid_filter", err.Error(), "window")
		return
	}
	now := time.Now().UTC()
	from := now.Add(-window)
	filter := storage.EventFilter{From: from, To: now}
	store := storage.NewRoutingLog(h.db)

	// The total and the two mode groups come from one aggregate over the window, so
	// the distribution and the total cannot disagree.
	modeStats, err := store.Stats(r.Context(), storage.StatsQuery{Filter: filter, GroupBy: storage.GroupByRoutingMode})
	if err != nil {
		storageFailure(w, h, "aggregate routing events by mode", err)
		return
	}
	// The latency and token aggregates are the unfiltered total, which is the row
	// for the whole window.
	overall, err := store.Stats(r.Context(), storage.StatsQuery{Filter: filter})
	if err != nil {
		storageFailure(w, h, "aggregate routing events", err)
		return
	}
	// The auto-only aggregate supplies the automatic latency. The two remaining auto
	// denominators come from the mode distribution above and from the scoreboard.
	autoFilter := storage.EventFilter{From: from, To: now, RoutingMode: logging.RoutingModeAuto}
	autoOverall, err := store.Stats(r.Context(), storage.StatsQuery{Filter: autoFilter})
	if err != nil {
		storageFailure(w, h, "aggregate automatic routing events", err)
		return
	}
	scores, err := store.ModelScoreboard(r.Context(), from, now, maxSummaryModels)
	if err != nil {
		storageFailure(w, h, "read the model scoreboard", err)
		return
	}
	recentPage, err := store.SelectEvents(r.Context(), filter, storage.EventCursor{}, summaryRecentLimit)
	if err != nil {
		storageFailure(w, h, "read recent routing events", err)
		return
	}

	payload := summaryPayload{}
	payload.Window = summaryWindowPayload{
		Key:  key,
		From: from.Format(time.RFC3339Nano),
		To:   now.Format(time.RFC3339Nano),
	}
	// The request distribution. The total is the sum of the two mode groups rather
	// than the overall count, because a request rejected before its mode was known
	// (an unconfigured Provider, a rejected media type) has no mode and is not part
	// of either numerator. Reporting it in the total but in neither group would make
	// auto_usage_rate a ratio over a denominator it does not belong to.
	total := int64(0)
	for _, group := range modeStats {
		switch group.Group {
		case logging.RoutingModeAuto:
			payload.Requests.Auto = group.Count
		case logging.RoutingModeExplicit:
			payload.Requests.Explicit = group.Count
		}
		total += group.Count
	}
	payload.Requests.Total = total

	// The Jev invocation count and the two adoption counts come from the storage
	// layer's own metric queries, so every predicate that defines a rate lives next
	// to the scoreboard that reports the same numbers.
	jevOK, err := store.CountJevOK(r.Context(), from, now)
	if err != nil {
		storageFailure(w, h, "count successful Jev calls", err)
		return
	}
	decisionsWithModel := int64(0)
	for _, score := range scores {
		decisionsWithModel += score.Decisions
	}
	recommendations, err := store.CountRecordedTop1(r.Context(), from, now)
	if err != nil {
		storageFailure(w, h, "count recorded recommendations", err)
		return
	}
	adopted, err := store.CountAdoptedTop1(r.Context(), from, now)
	if err != nil {
		storageFailure(w, h, "count adopted recommendations", err)
		return
	}
	if adopted > recommendations {
		// The numerator is a subset of the denominator by construction: both are
		// counts over the same window with the adoption count adding one equality.
		// A larger numerator would mean the two predicates disagree, which is a bug
		// rather than a finding, so it is reported instead of published.
		h.logger.Error("the adoption numerator exceeds its denominator",
			"adopted", adopted, "recommendations", recommendations)
		storageFailure(w, h, "read the adoption counts", errAdoptionInconsistent)
		return
	}

	payload.Rates = []ratePayload{
		ratio("client_request_success_rate", sumField(overall, func(s storage.EventStats) int64 { return s.Success }), sumField(overall, func(s storage.EventStats) int64 { return s.Count }), "client requests with a final 2xx status", "client requests"),
		ratio("auto_usage_rate", payload.Requests.Auto, payload.Requests.Total,
			"automatic requests", "requests that reached the forwarding path"),
		ratio("auto_decision_success_rate", decisionsWithModel, payload.Requests.Auto,
			"automatic requests that produced a model", "automatic requests"),
		ratio("jev_invocation_rate", jevOK, payload.Requests.Auto,
			"automatic requests whose Jev call succeeded", "automatic requests"),
		ratio("jev_top1_adoption_rate", adopted, recommendations,
			"decisions whose final model was the Jev top-1", "decisions with a recorded Jev top-1"),
	}

	payload.Latency.All = latencyOf(overall)
	payload.Latency.Auto = latencyOf(autoOverall)
	payload.Tokens = tokensOf(overall)
	payload.AttemptOutputTokensPerSecond, err = store.OutputTokensPerSecond(r.Context(), now)
	if err != nil {
		storageFailure(w, h, "aggregate recent attempt output tokens", err)
		return
	}
	payload.Statuses = summaryStatuses{
		Success:     sumField(overall, func(s storage.EventStats) int64 { return s.Success }),
		ClientError: sumField(overall, func(s storage.EventStats) int64 { return s.ClientError }),
		ServerError: sumField(overall, func(s storage.EventStats) int64 { return s.ServerError }),
	}
	payload.Models = make([]summaryModel, 0, len(scores))
	for _, score := range scores {
		payload.Models = append(payload.Models, summaryModel{
			Model:           score.Model,
			Decisions:       score.Decisions,
			AdoptedTop1:     score.AdoptedTop1,
			RecommendedTop1: score.RecommendedTop1,
		})
	}
	payload.Recent = make([]logEventPayload, 0, len(recentPage.Events))
	for i := range recentPage.Events {
		payload.Recent = append(payload.Recent, logEventPayloadOf(recentPage.Events[i]))
	}
	if h.recorder != nil {
		payload.LogDisabled = !h.recorder.Stats().Enabled
	}
	writeAdminJSON(w, http.StatusOK, payload)
}

// latencyOf renders one aggregate row as a latency class.
func latencyOf(groups []storage.EventStats) summaryLatency {
	latency := summaryLatency{}
	if len(groups) == 0 {
		return latency
	}
	group := groups[0]
	latency.Count = group.Count
	latency.MeanDurationMS = group.MeanDurationMS
	latency.P95DurationMS = group.P95DurationMS
	return latency
}

// tokensOf renders one aggregate row as the token report.
func tokensOf(groups []storage.EventStats) summaryTokens {
	tokens := summaryTokens{}
	if len(groups) == 0 {
		return tokens
	}
	group := groups[0]
	tokens.InputTokens = group.InputTokens
	tokens.OutputTokens = group.OutputTokens
	tokens.TotalTokens = group.TotalTokens
	tokens.ObservedUsageCount = group.ObservedUsageCount
	tokens.InputObservedCount = group.InputTokensObserved
	tokens.OutputObservedCount = group.OutputObserved
	tokens.TotalObservedCount = group.TotalTokensObserved
	return tokens
}

// sumField folds one per-group field into a total over a single-row aggregate.
func sumField(groups []storage.EventStats, read func(storage.EventStats) int64) int64 {
	total := int64(0)
	for _, group := range groups {
		total += read(group)
	}
	return total
}
