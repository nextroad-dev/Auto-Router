package api

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/nextroad-dev/Auto-Router/internal/logging"
	"github.com/nextroad-dev/Auto-Router/internal/models"
	"github.com/nextroad-dev/Auto-Router/internal/storage"
)

// This file is the read-only half of the management surface that is not the
// registry: the routing log, the effective settings, the health report and the
// synchronization state.
//
// Two properties are worth stating once:
//
//   - The log query is the same schema the writer writes and the -logs-check mode
//     reads. There is no second definition of "what an event is", which is what
//     keeps the management view and the audit trail from drifting apart.
//   - A filter value is validated against internal/logging's own closed sets before
//     it reaches SQL, and a value that is not a member produces a 400 that names the
//     parameter without repeating the value. A request that matches nothing is a 200
//     with an empty page, which is a different answer from a refused query.

// logEventPayload is one stored event as the management surface reports it. Every
// pointer field is a column that can be NULL, and NULL is reported as JSON null
// rather than as a zero: "the upstream did not report this" is not "zero".
type logEventPayload struct {
	ID                int64    `json:"id"`
	RequestID         string   `json:"request_id"`
	StartedAt         string   `json:"started_at"`
	DurationMS        int64    `json:"duration_ms"`
	Protocol          string   `json:"protocol"`
	RoutingMode       *string  `json:"routing_mode"`
	SelectionMode     *string  `json:"selection_mode"`
	RequestedModel    *string  `json:"requested_model"`
	EffectiveModel    *string  `json:"effective_model"`
	JevTopModel       *string  `json:"jev_top_model"`
	ProviderKey       *string  `json:"provider"`
	UpstreamModel     *string  `json:"upstream_model"`
	Status            int      `json:"status"`
	UpstreamStatus    *int     `json:"upstream_status"`
	ErrorCode         *string  `json:"error_code"`
	Stream            bool     `json:"stream"`
	BytesWritten      int64    `json:"bytes_written"`
	ClientIP          *string  `json:"client_ip"`
	RoutingPreference *string  `json:"routing_preference"`
	PreferenceSource  *string  `json:"preference_source"`
	JevStatus         *string  `json:"jev_status"`
	Confidence        *float64 `json:"confidence"`
	ConfidenceBand    *string  `json:"confidence_band"`
	FallbackReason    *string  `json:"fallback_reason"`
	EvidenceHash      *string  `json:"evidence_hash"`
	GatewayAttempts   int      `json:"gateway_attempts"`
	FailoverUsed      bool     `json:"failover_used"`
	RoutingLatencyMS  *int64   `json:"routing_latency_ms"`
	JevLatencyMS      *int64   `json:"jev_latency_ms"`
	InputTokens       *int64   `json:"input_tokens"`
	OutputTokens      *int64   `json:"output_tokens"`
	TotalTokens       *int64   `json:"total_tokens"`
	UsageStatus       string   `json:"usage_status"`
	UsageSource       *string  `json:"usage_source"`
	// JevTrace is present only when a trace was stored for this request, which is
	// what makes the request_id filter the one way to read a detail view: the trace
	// belongs to the event, not to a second identifier namespace.
	JevTrace *jevTracePayload    `json:"jev_trace,omitempty"`
	Attempts []logAttemptPayload `json:"attempts,omitempty"`
}

type logAttemptPayload struct {
	ID           int64   `json:"id"`
	Index        int     `json:"attempt_index"`
	Group        *string `json:"group_name"`
	Provider     *string `json:"provider"`
	Model        *string `json:"model_id"`
	StartedAt    string  `json:"started_at"`
	CompletedAt  *string `json:"completed_at"`
	Status       *int    `json:"status"`
	ErrorCode    *string `json:"error_code"`
	InputTokens  *int64  `json:"input_tokens"`
	OutputTokens *int64  `json:"output_tokens"`
	TotalTokens  *int64  `json:"total_tokens"`
	UsageStatus  string  `json:"usage_status"`
}

// jevTracePayload is the stored Jev trace. It carries identifiers, counts,
// durations and the normalized distribution, exactly as the table does.
type jevTracePayload struct {
	Status          string             `json:"status"`
	FailureReason   *string            `json:"failure_reason"`
	InputMode       string             `json:"input_mode"`
	LatencyMS       *int64             `json:"latency_ms"`
	CandidateCount  int                `json:"candidate_count"`
	CandidateModels []string           `json:"candidate_models"`
	ModelCount      int                `json:"model_count"`
	SelectedModel   *string            `json:"selected_model"`
	Confidence      *float64           `json:"confidence"`
	ConfidenceBand  *string            `json:"confidence_band"`
	FallbackReason  *string            `json:"fallback_reason"`
	EvidenceHash    *string            `json:"evidence_hash"`
	Probabilities   []modelProbability `json:"probabilities"`
}

type modelProbability struct {
	Model       string  `json:"model"`
	Probability float64 `json:"probability"`
}

// handleLogList serves GET /admin/v1/logs with the documented filters.
func (h *adminHandler) handleLogList(w http.ResponseWriter, r *http.Request) {
	if !h.requireDatabase(w) {
		return
	}
	limit, err := h.pageLimit(r)
	if err != nil {
		writeAdminError(w, http.StatusBadRequest, "invalid_request", err.Error(), "limit")
		return
	}
	filter, filterErr := h.logFilter(r.URL.Query())
	if filterErr != nil {
		writeAdminError(w, http.StatusBadRequest, "invalid_filter", filterErr.message, filterErr.field)
		return
	}
	fields, err := decodeCursor(r.URL.Query().Get("cursor"), cursorLog, 2)
	if err != nil {
		writeAdminError(w, http.StatusBadRequest, "invalid_cursor", "the cursor is not valid for this endpoint", "")
		return
	}
	cursor := storage.EventCursor{}
	if fields[0] != "" {
		started, parseErr := time.Parse(time.RFC3339Nano, fields[0])
		id, idErr := strconv.ParseInt(fields[1], 10, 64)
		if parseErr != nil || idErr != nil {
			writeAdminError(w, http.StatusBadRequest, "invalid_cursor", "the cursor is not valid for this endpoint", "")
			return
		}
		cursor.StartedAt, cursor.ID = started, id
	}
	store := storage.NewRoutingLog(h.db)
	page, err := store.SelectEvents(r.Context(), filter, cursor, limit)
	if err != nil {
		storageFailure(w, h, "read routing events", err)
		return
	}
	payload := make([]logEventPayload, 0, len(page.Events))
	for i := range page.Events {
		entry := page.Events[i]
		item := logEventPayloadOf(entry)
		attempts, attemptErr := store.SelectAttemptsForRequest(r.Context(), entry.Event.RequestID)
		if attemptErr != nil {
			storageFailure(w, h, "read routing attempts", attemptErr)
			return
		}
		for _, stored := range attempts {
			a := stored.Attempt
			item.Attempts = append(item.Attempts, logAttemptPayload{ID: stored.ID, Index: a.AttemptIndex, Group: optionalString(a.GroupName), Provider: optionalString(a.ProviderKey), Model: optionalString(a.ModelID), StartedAt: a.StartedAt.UTC().Format(time.RFC3339Nano), CompletedAt: formatOptionalTime(a.CompletedAt), Status: optionalStatus(a.Status), ErrorCode: optionalString(a.ErrorCode), InputTokens: a.InputTokens, OutputTokens: a.OutputTokens, TotalTokens: a.TotalTokens, UsageStatus: a.UsageStatus})
		}
		// The trace is fetched per event only when one may exist, so a page of
		// events without traces costs one query rather than one per row in the worst
		// case. The column is what the schema knows; a missing trace is simply absent.
		if entry.Event.JevStatus != "" && entry.Event.JevStatus != logging.JevStatusDisabled {
			trace, traceErr := store.SelectTraceFor(r.Context(), entry.Event.RequestID)
			if traceErr != nil {
				storageFailure(w, h, "read routing trace", traceErr)
				return
			}
			if trace != nil {
				item.JevTrace = jevTracePayloadOf(trace)
			}
		}
		payload = append(payload, item)
	}
	var next *string
	if page.More && len(page.Events) > 0 {
		last := page.Events[len(page.Events)-1]
		value := encodeCursor(cursorLog, last.Event.StartedAt.UTC().Format(time.RFC3339Nano), strconv.FormatInt(last.ID, 10))
		next = &value
	}
	writeAdminJSON(w, http.StatusOK, pageOf(payload, next))
}

// filterError is one refused filter. It carries the parameter name so the response
// can name the field without repeating its value.
type filterError struct {
	field   string
	message string
}

func refuseFilter(field, message string) *filterError {
	return &filterError{field: field, message: message}
}

// logFilter builds the storage filter from the query string, validating every
// closed-set value against internal/logging's own validators.
func (h *adminHandler) logFilter(query url.Values) (storage.EventFilter, *filterError) {
	filter := storage.EventFilter{
		ProviderKey: query.Get("provider"),
		Model:       query.Get("model"),
		RequestID:   query.Get("request_id"),
		ErrorCode:   query.Get("error_code"),
		Protocol:    query.Get("protocol"),
	}
	search, valid := adminSearchTerm(query.Get("search"))
	if !valid {
		return filter, refuseFilter("search", "the search filter is invalid")
	}
	filter.Search = search
	if filter.ProviderKey != "" {
		if err := models.ValidateProviderKey(filter.ProviderKey); err != nil {
			return filter, refuseFilter("provider", "the provider filter is not a provider key")
		}
	}
	if filter.Model != "" {
		if err := models.ValidateModelID(filter.Model); err != nil {
			return filter, refuseFilter("model", "the model filter is not a model identifier")
		}
	}
	if filter.RequestID != "" && !validIdentifierValue(filter.RequestID) {
		return filter, refuseFilter("request_id", "the request_id filter is not a request identifier")
	}
	if filter.ErrorCode != "" && !validIdentifierValue(filter.ErrorCode) {
		return filter, refuseFilter("error_code", "the error_code filter is not an identifier")
	}
	if filter.Protocol != "" {
		switch filter.Protocol {
		case "chat_completions", "responses", "native":
		default:
			return filter, refuseFilter("protocol", "the protocol filter is not supported")
		}
	}
	for _, closed := range []struct {
		field   string
		value   string
		valid   func(string) bool
		refusal string
	}{
		{"routing_mode", query.Get("routing_mode"), func(value string) bool {
			return value == logging.RoutingModeAuto || value == logging.RoutingModeExplicit
		}, "the routing_mode filter is not one of the two routing modes"},
		{"selection_mode", query.Get("selection_mode"), logging.ValidSelectionMode, "the selection_mode filter is not a known selection mode"},
		{"jev_status", query.Get("jev_status"), logging.ValidJevStatus, "the jev_status filter is not a known Jev status"},
		{"confidence_band", query.Get("confidence_band"), logging.ValidConfidenceBand, "the confidence_band filter is not a known band"},
		{"fallback_reason", query.Get("fallback_reason"), logging.ValidFallbackReason, "the fallback_reason filter is not a known reason"},
		{"usage_status", query.Get("usage_status"), logging.ValidUsageStatus, "the usage_status filter is not a known usage status"},
		{"status_class", query.Get("status_class"), storage.ValidStatusClass, "the status_class filter is not a known status class"},
	} {
		if closed.value == "" {
			continue
		}
		if !closed.valid(closed.value) {
			return filter, refuseFilter(closed.field, closed.refusal)
		}
		switch closed.field {
		case "routing_mode":
			filter.RoutingMode = closed.value
		case "selection_mode":
			filter.SelectionMode = closed.value
		case "jev_status":
			filter.JevStatus = closed.value
		case "confidence_band":
			filter.ConfidenceBand = closed.value
		case "fallback_reason":
			filter.FallbackReason = closed.value
		case "usage_status":
			filter.UsageStatus = closed.value
		case "status_class":
			filter.StatusClass = closed.value
		}
	}
	if raw := query.Get("status"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 100 || parsed > 599 {
			return filter, refuseFilter("status", "the status filter must be a three digit HTTP status")
		}
		filter.Status = parsed
	}
	stream, err := parseBoolParam(query.Get("stream"))
	if err != nil {
		return filter, refuseFilter("stream", err.Error())
	}
	filter.Stream = stream
	if filter.From, err = parseTimeParam(query.Get("from")); err != nil {
		return filter, refuseFilter("from", err.Error())
	}
	if filter.To, err = parseTimeParam(query.Get("to")); err != nil {
		return filter, refuseFilter("to", err.Error())
	}
	if !filter.From.IsZero() && !filter.To.IsZero() && filter.To.Before(filter.From) {
		// An inverted range is a mistake, and answering it with an empty page would
		// look like "nothing happened" rather than "you asked the wrong question".
		return filter, refuseFilter("to", "the to bound must not be earlier than the from bound")
	}
	return filter, nil
}

// validIdentifierValue reports whether a value is a log-safe identifier. It mirrors
// the logging package's own rule, so a filter cannot accept a value the writer would
// have refused to store.
func validIdentifierValue(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for i := 0; i < len(value); i++ {
		c := value[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case c == '.' || c == '_' || c == '-' || c == ':':
		default:
			return false
		}
	}
	return true
}

// logStatsPayload is one aggregate group as the management surface reports it.
type logStatsPayload struct {
	Group                *string  `json:"group"`
	Count                int64    `json:"count"`
	MeanDurationMS       *float64 `json:"mean_duration_ms"`
	P95DurationMS        *int64   `json:"p95_duration_ms"`
	InputTokens          *int64   `json:"input_tokens"`
	OutputTokens         *int64   `json:"output_tokens"`
	TotalTokens          *int64   `json:"total_tokens"`
	ObservedUsageCount   int64    `json:"observed_usage_count"`
	TokensObserved       int64    `json:"tokens_observed_count"`
	InputTokensObserved  int64    `json:"input_tokens_observed_count"`
	OutputTokensObserved int64    `json:"output_tokens_observed_count"`
	TotalTokensObserved  int64    `json:"total_tokens_observed_count"`
	Success              int64    `json:"status_success"`
	ClientError          int64    `json:"status_client_error"`
	ServerError          int64    `json:"status_server_error"`
}

// handleLogStats serves GET /admin/v1/logs/stats.
func (h *adminHandler) handleLogStats(w http.ResponseWriter, r *http.Request) {
	if !h.requireDatabase(w) {
		return
	}
	query := r.URL.Query()
	filter, filterErr := h.logFilter(query)
	if filterErr != nil {
		writeAdminError(w, http.StatusBadRequest, "invalid_filter", filterErr.message, filterErr.field)
		return
	}
	groupBy := query.Get("group_by")
	store := storage.NewRoutingLog(h.db)
	if groupBy == "attempt_group" || groupBy == "attempt_model" {
		attemptGroups, attemptErr := store.AttemptStats(r.Context(), storage.AttemptStatsQuery{From: filter.From, To: filter.To, GroupBy: strings.TrimPrefix(groupBy, "attempt_")})
		if attemptErr != nil {
			storageFailure(w, h, "aggregate routing attempts", attemptErr)
			return
		}
		payload := make([]logStatsPayload, 0, len(attemptGroups))
		for _, g := range attemptGroups {
			label := g.Group
			payload = append(payload, logStatsPayload{Group: &label, Count: g.Count, InputTokens: g.InputTokens, OutputTokens: g.OutputTokens, TotalTokens: g.TotalTokens, TokensObserved: g.OutputObserved, InputTokensObserved: g.InputObserved, OutputTokensObserved: g.OutputObserved, TotalTokensObserved: g.TotalObserved})
		}
		writeAdminJSON(w, http.StatusOK, struct {
			GroupBy     string            `json:"group_by"`
			Groups      []logStatsPayload `json:"groups"`
			Granularity string            `json:"granularity"`
		}{groupBy, payload, "upstream_attempt"})
		return
	}
	if groupBy != "" && !storage.ValidGroupBy(groupBy) {
		writeAdminError(w, http.StatusBadRequest, "invalid_filter", "the group_by value is not one of the documented groupings", "group_by")
		return
	}
	groups, err := store.Stats(r.Context(), storage.StatsQuery{Filter: filter, GroupBy: groupBy})
	if err != nil {
		storageFailure(w, h, "aggregate routing events", err)
		return
	}
	payload := make([]logStatsPayload, 0, len(groups))
	for _, group := range groups {
		item := logStatsPayload{
			Count:                group.Count,
			MeanDurationMS:       group.MeanDurationMS,
			P95DurationMS:        group.P95DurationMS,
			InputTokens:          group.InputTokens,
			OutputTokens:         group.OutputTokens,
			TotalTokens:          group.TotalTokens,
			ObservedUsageCount:   group.ObservedUsageCount,
			TokensObserved:       group.InputTokensObserved,
			InputTokensObserved:  group.InputTokensObserved,
			OutputTokensObserved: group.OutputObserved,
			TotalTokensObserved:  group.TotalTokensObserved,
			Success:              group.Success,
			ClientError:          group.ClientError,
			ServerError:          group.ServerError,
		}
		if groupBy != "" {
			label := group.Group
			item.Group = &label
		}
		payload = append(payload, item)
	}
	writeAdminJSON(w, http.StatusOK, struct {
		GroupBy string            `json:"group_by"`
		Groups  []logStatsPayload `json:"groups"`
	}{GroupBy: groupBy, Groups: payload})
}

func formatOptionalTime(value *time.Time) *string {
	if value == nil {
		return nil
	}
	formatted := value.UTC().Format(time.RFC3339Nano)
	return &formatted
}
func optionalStatus(value int) *int {
	if value == 0 {
		return nil
	}
	return &value
}

// handleSyncState serves GET /admin/v1/sync-state.
func (h *adminHandler) handleSyncState(w http.ResponseWriter, r *http.Request) {
	if !h.requireDatabase(w) {
		return
	}
	state, found, err := storage.LoadSyncState(r.Context(), h.db, models.SourceModelsDev)
	if err != nil {
		storageFailure(w, h, "read sync state", err)
		return
	}
	if !found {
		// "Never synchronized" is a fact worth reporting with a 200: the client's
		// remedy is the explicit WebUI sync action, and a 404 would suggest the
		// endpoint itself is missing.
		writeAdminJSON(w, http.StatusOK, struct {
			Synchronized bool `json:"synchronized"`
		}{Synchronized: false})
		return
	}
	warnings := state.Warnings
	if warnings == nil {
		warnings = []string{}
	}
	writeAdminJSON(w, http.StatusOK, struct {
		Synchronized    bool     `json:"synchronized"`
		URL             string   `json:"url"`
		FetchedAt       string   `json:"fetched_at"`
		AllowlistDigest string   `json:"allowlist_digest"`
		ImportedPairs   int      `json:"imported_pairs"`
		SkippedPairs    int      `json:"skipped_pairs"`
		Warnings        []string `json:"warnings"`
	}{
		Synchronized:    true,
		URL:             state.URL,
		FetchedAt:       state.FetchedAt.UTC().Format(time.RFC3339Nano),
		AllowlistDigest: state.AllowlistDigest,
		ImportedPairs:   state.ImportedPairs,
		SkippedPairs:    state.SkippedPairs,
		Warnings:        warnings,
	})
}

// logEventPayloadOf converts one stored event into its reported shape. The mapping
// is total: every column has exactly one field, and a NULL column becomes a nil
// pointer rather than a zero value.
func logEventPayloadOf(entry storage.StoredEvent) logEventPayload {
	event := entry.Event
	payload := logEventPayload{
		ID:               entry.ID,
		RequestID:        event.RequestID,
		StartedAt:        event.StartedAt.UTC().Format(time.RFC3339Nano),
		DurationMS:       event.DurationMS,
		Protocol:         event.Protocol,
		Status:           event.Status,
		Stream:           event.Stream,
		BytesWritten:     event.BytesWritten,
		GatewayAttempts:  event.GatewayAttempts,
		FailoverUsed:     event.FailoverUsed,
		UsageStatus:      event.Usage.Status,
		RoutingLatencyMS: event.RoutingLatencyMS,
		JevLatencyMS:     event.JevLatencyMS,
		InputTokens:      event.Usage.InputTokens,
		OutputTokens:     event.Usage.OutputTokens,
		TotalTokens:      event.Usage.TotalTokens,
	}
	payload.RoutingMode = optionalString(event.RoutingMode)
	payload.SelectionMode = optionalString(event.SelectionMode)
	payload.RequestedModel = optionalString(event.RequestedModel)
	payload.EffectiveModel = optionalString(event.EffectiveModel)
	payload.JevTopModel = optionalString(event.JevTopModel)
	payload.ProviderKey = optionalString(event.ProviderKey)
	payload.UpstreamModel = optionalString(event.UpstreamModel)
	payload.ErrorCode = optionalString(event.ErrorCode)
	payload.ClientIP = optionalString(event.ClientIP)
	payload.RoutingPreference = optionalString(event.RoutingPreference)
	payload.PreferenceSource = optionalString(event.PreferenceSource)
	payload.JevStatus = optionalString(event.JevStatus)
	payload.ConfidenceBand = optionalString(event.ConfidenceBand)
	payload.FallbackReason = optionalString(event.FallbackReason)
	payload.EvidenceHash = optionalString(event.EvidenceHash)
	payload.Confidence = event.Confidence
	payload.UsageSource = optionalString(event.Usage.Source)
	if event.UpstreamStatus != 0 {
		status := event.UpstreamStatus
		payload.UpstreamStatus = &status
	}
	return payload
}

// jevTracePayloadOf converts one stored trace into its reported shape.
func jevTracePayloadOf(trace *logging.JevTrace) *jevTracePayload {
	if trace == nil {
		return nil
	}
	payload := &jevTracePayload{
		Status:          trace.Status,
		FailureReason:   optionalString(trace.FailureReason),
		InputMode:       trace.InputMode,
		LatencyMS:       trace.LatencyMS,
		CandidateCount:  trace.CandidateCount,
		CandidateModels: append([]string{}, trace.CandidateModels...),
		ModelCount:      trace.ModelCount,
		SelectedModel:   optionalString(trace.Selected),
		Confidence:      trace.Confidence,
		ConfidenceBand:  optionalString(trace.ConfidenceBand),
		FallbackReason:  optionalString(trace.FallbackReason),
		EvidenceHash:    optionalString(trace.EvidenceHash),
		Probabilities:   make([]modelProbability, 0, len(trace.Probabilities)),
	}
	for _, probability := range trace.Probabilities {
		payload.Probabilities = append(payload.Probabilities, modelProbability{
			Model:       probability.Model,
			Probability: probability.Probability,
		})
	}
	return payload
}

// optionalString maps an empty stored string onto a JSON null. No identifier in this
// schema has an empty value that means something, so "" and "absent" are one fact.
func optionalString(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

// handleHealth serves GET /admin/v1/health.
func (h *adminHandler) handleHealth(w http.ResponseWriter, r *http.Request) {
	catalog := h.catalog.Load()
	health := struct {
		Status    string `json:"status"`
		UptimeMS  int64  `json:"uptime_ms"`
		StartedAt string `json:"started_at"`
		Catalog   struct {
			Generation uint64 `json:"generation"`
			Providers  struct {
				Total   int `json:"total"`
				Enabled int `json:"enabled"`
			} `json:"providers"`
			Models struct {
				Total   int `json:"total"`
				Enabled int `json:"enabled"`
			} `json:"models"`
			Pairs struct {
				Total   int `json:"total"`
				Enabled int `json:"enabled"`
			} `json:"pairs"`
		} `json:"catalog"`
		RoutingLog struct {
			Enabled       bool   `json:"enabled"`
			QueueDepth    int    `json:"queue_depth"`
			QueueCapacity int    `json:"queue_capacity"`
			Written       uint64 `json:"written"`
			Dropped       uint64 `json:"dropped"`
			Failed        uint64 `json:"failed"`
		} `json:"routing_log"`
		Storage  *string `json:"storage"`
		Settings struct {
			Version int64 `json:"version"`
			Overlay bool  `json:"overlay"`
		} `json:"settings"`
		// Sessions reports the management surface's browser sessions: how many are
		// live and how long one lasts. It never reports a token or a key name.
		Sessions sessionReport `json:"sessions"`
	}{}
	health.Status = "ok"
	if !h.startedAt.IsZero() {
		health.UptimeMS = time.Since(h.startedAt).Milliseconds()
		health.StartedAt = h.startedAt.UTC().Format(time.RFC3339Nano)
	}
	health.Catalog.Generation = catalog.Generation
	health.Catalog.Providers.Total = len(catalog.Providers)
	health.Catalog.Providers.Enabled = catalog.EnabledProviders()
	health.Catalog.Models.Total = len(catalog.Models)
	health.Catalog.Models.Enabled = catalog.EnabledModels()
	health.Catalog.Pairs.Total = len(catalog.Pairs)
	health.Catalog.Pairs.Enabled = catalog.EnabledPairs()
	if h.recorder != nil {
		stats := h.recorder.Stats()
		health.RoutingLog.Enabled = stats.Enabled
		health.RoutingLog.QueueDepth = stats.QueueDepth
		health.RoutingLog.QueueCapacity = stats.QueueCapacity
		health.RoutingLog.Written = stats.Written
		health.RoutingLog.Dropped = stats.Dropped
		health.RoutingLog.Failed = stats.Failed
	}
	if h.settings != nil {
		health.Settings.Version = h.settings.Current().Version
		health.Settings.Overlay = h.settings.HasOverlay()
	}
	health.Sessions = h.sessionsReport()
	// The database is probed last and only when one is wired, so the report itself
	// never fails: "not ready" is a field of a 200, not a 503. An administrator
	// asking "why is the service not ready" needs the answer to arrive.
	if h.readiness != nil {
		ctx, cancel := contextWithTimeout(r, 2*time.Second)
		defer cancel()
		if err := h.readiness.PingContext(ctx); err != nil {
			status := "not_ready"
			health.Status = status
			health.Storage = &status
		} else {
			status := "ready"
			health.Storage = &status
		}
	}
	writeAdminJSON(w, http.StatusOK, health)
}

// contextWithTimeout derives a bounded context from one request. It is a helper
// rather than a call to context.WithTimeout at each site so the two places that need
// it cannot choose different bounds.
func contextWithTimeout(r *http.Request, timeout time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(r.Context(), timeout)
}
