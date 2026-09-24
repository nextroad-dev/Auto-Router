package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/nextroad-dev/Auto-Router/internal/logging"
)

// routingEventColumns is the column list of routing_events in the order every
// statement and scan uses. It is one constant so a query cannot drift from the
// schema in one place only.
const routingEventColumns = `request_id, started_at, duration_ms, protocol, routing_mode, selection_mode, requested_model,
	provider_key, upstream_model, status, upstream_status, error_code, stream, bytes_written, client_ip,
	routing_preference, preference_source, jev_status, confidence, confidence_band, fallback_reason, evidence_hash,
	gateway_attempts, failover_used, routing_latency_ms, jev_latency_ms,
	input_tokens, output_tokens, total_tokens, usage_status, usage_source, effective_model, jev_top_model`

// routingEventPlaceholders is one "?" per column of routingEventColumns, in the
// same order.
const routingEventPlaceholders = `?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?`

// RoutingLog is the routing log store: batched inserts through the single
// connection pool and bounded retention deletes. Reads are deliberately limited
// to the one query the -logs-check maintenance mode needs; the future Admin API
// owns its own pagination and filtering, so this type cannot become a second,
// competing definition of what the stored columns mean.
//
// The type is not safe to mutate concurrently: Logger is set once, before the
// writer goroutine starts.
type RoutingLog struct {
	db *sql.DB
	// Logger receives one WARN when a diagnostic field has to be replaced because
	// it exceeded its documented bound. The writer refuses such an event before it
	// reaches here, so this is a defensive path rather than an expected outcome.
	// Nil means the replacement is silent.
	Logger *slog.Logger
}

// NewRoutingLog builds the store. It never returns nil; a store built on a nil
// pool fails every operation instead of panicking on the request path.
func NewRoutingLog(db *sql.DB) *RoutingLog { return &RoutingLog{db: db} }

// Insert writes one batch of events in a single transaction. Each event and its
// optional Jev trace are written together, so a trace row can never exist without
// the event it belongs to. Validation happens before the transaction opens: a
// rejected batch leaves the database untouched and is reported as an error, which
// the async writer turns into a counted drop.
func (l *RoutingLog) Insert(ctx context.Context, events []logging.Event) error {
	if len(events) == 0 {
		return nil
	}
	if l.db == nil {
		return errors.New("the routing log has no database")
	}
	rows := make([]storedRow, len(events))
	for i, event := range events {
		if err := event.Valid(); err != nil {
			return fmt.Errorf("routing event %q is not storable: %w", event.RequestID, err)
		}
		rows[i] = newStoredRow(event)
	}
	tx, err := l.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin routing log batch: %w", err)
	}
	defer tx.Rollback()
	insert, err := tx.PrepareContext(ctx, "INSERT INTO routing_events ("+routingEventColumns+") VALUES ("+routingEventPlaceholders+")")
	if err != nil {
		return fmt.Errorf("prepare routing event insert: %w", err)
	}
	defer insert.Close()
	for _, row := range rows {
		if _, err := insert.ExecContext(ctx, row.values...); err != nil {
			return fmt.Errorf("insert routing event %q: %w", row.requestID, err)
		}
	}
	trace, err := tx.PrepareContext(ctx, `INSERT INTO routing_jev_calls
		(request_id, started_at, status, failure_reason, input_mode, latency_ms, candidate_count, candidates_json,
		 model_count, selected_model, confidence, confidence_band, fallback_reason, distribution_json, evidence_hash)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return fmt.Errorf("prepare routing trace insert: %w", err)
	}
	defer trace.Close()
	for _, row := range rows {
		if row.trace == nil {
			continue
		}
		if _, err := trace.ExecContext(ctx, row.trace.values(row.requestID, row.startedAt, l.Logger)...); err != nil {
			return fmt.Errorf("insert routing trace for %q: %w", row.requestID, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit routing log batch: %w", err)
	}
	return nil
}

// InsertAttempts stores a batch of upstream attempts atomically. Each attempt is
// a metadata-only record; no request or response content is accepted here.
func (l *RoutingLog) InsertAttempts(ctx context.Context, attempts []logging.Attempt) error {
	if len(attempts) == 0 {
		return nil
	}
	if l.db == nil {
		return errors.New("the routing log has no database")
	}
	for _, a := range attempts {
		if err := a.Valid(); err != nil {
			return fmt.Errorf("routing attempt %q/%d is not storable: %w", a.RequestID, a.AttemptIndex, err)
		}
	}
	tx, err := l.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin routing attempt batch: %w", err)
	}
	defer tx.Rollback()
	statement, err := tx.PrepareContext(ctx, `INSERT INTO routing_attempts
		(request_id, attempt_index, group_name, provider_key, model_id, started_at, completed_at, status, error_code,
		 input_tokens, output_tokens, total_tokens, usage_status)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return fmt.Errorf("prepare routing attempt insert: %w", err)
	}
	defer statement.Close()
	for _, a := range attempts {
		var completed any
		if a.CompletedAt != nil {
			completed = formatTimestamp(*a.CompletedAt)
		}
		var status any
		if a.Status != 0 {
			status = a.Status
		}
		_, err := statement.ExecContext(ctx, a.RequestID, a.AttemptIndex, nullString(a.GroupName), nullString(a.ProviderKey), nullString(a.ModelID),
			formatTimestamp(a.StartedAt), completed, status, nullString(a.ErrorCode), nullInt64(a.InputTokens), nullInt64(a.OutputTokens), nullInt64(a.TotalTokens), a.UsageStatus)
		if err != nil {
			return fmt.Errorf("insert routing attempt %q/%d: %w", a.RequestID, a.AttemptIndex, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit routing attempt batch: %w", err)
	}
	return nil
}

// DeleteAttemptsBefore removes a bounded batch by attempt start time.
func (l *RoutingLog) DeleteAttemptsBefore(ctx context.Context, cutoff time.Time, limit int) (int, error) {
	return l.deleteBefore(ctx, "routing_attempts", cutoff, limit)
}

// AttemptStatsQuery selects completed or started attempts in a time window and
// optionally groups by selected group or actual model_id.
type AttemptStatsQuery struct {
	From, To time.Time
	GroupBy  string
}

type AttemptStats struct {
	Group                                        string
	Count                                        int64
	InputTokens, OutputTokens, TotalTokens       *int64
	InputObserved, OutputObserved, TotalObserved int64
}

// StoredAttempt is one detailed upstream try as read from routing_attempts.
type StoredAttempt struct {
	ID      int64
	Attempt logging.Attempt
}

// SelectAttemptsForRequest returns a request's attempts in execution order.
func (l *RoutingLog) SelectAttemptsForRequest(ctx context.Context, requestID string) ([]StoredAttempt, error) {
	if l.db == nil {
		return nil, errors.New("the routing log has no database")
	}
	rows, err := l.db.QueryContext(ctx, `SELECT id, request_id, attempt_index, group_name, provider_key, model_id, started_at, completed_at, status, error_code, input_tokens, output_tokens, total_tokens, usage_status FROM routing_attempts WHERE request_id=? ORDER BY attempt_index`, requestID)
	if err != nil {
		return nil, fmt.Errorf("read routing attempts: %w", err)
	}
	defer rows.Close()
	result := make([]StoredAttempt, 0)
	for rows.Next() {
		var e StoredAttempt
		var group, provider, model, completed, errorCode sql.NullString
		var status sql.NullInt64
		var in, out, total sql.NullInt64
		var started string
		if err := rows.Scan(&e.ID, &e.Attempt.RequestID, &e.Attempt.AttemptIndex, &group, &provider, &model, &started, &completed, &status, &errorCode, &in, &out, &total, &e.Attempt.UsageStatus); err != nil {
			return nil, fmt.Errorf("scan routing attempt: %w", err)
		}
		e.Attempt.GroupName = group.String
		e.Attempt.ProviderKey = provider.String
		e.Attempt.ModelID = model.String
		e.Attempt.ErrorCode = errorCode.String
		e.Attempt.StartedAt, err = time.Parse(time.RFC3339Nano, started)
		if err != nil {
			return nil, fmt.Errorf("parse attempt start time: %w", err)
		}
		if completed.Valid {
			t, parseErr := time.Parse(time.RFC3339Nano, completed.String)
			if parseErr != nil {
				return nil, fmt.Errorf("parse attempt completion time: %w", parseErr)
			}
			e.Attempt.CompletedAt = &t
		}
		if status.Valid {
			e.Attempt.Status = int(status.Int64)
		}
		e.Attempt.InputTokens = optionalInt64(in)
		e.Attempt.OutputTokens = optionalInt64(out)
		e.Attempt.TotalTokens = optionalInt64(total)
		result = append(result, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read routing attempts: %w", err)
	}
	return result, nil
}

// AttemptStats aggregates upstream attempts; sums stay nil when no attempt
// reported the corresponding count.
func (l *RoutingLog) AttemptStats(ctx context.Context, q AttemptStatsQuery) ([]AttemptStats, error) {
	if l.db == nil {
		return nil, errors.New("the routing log has no database")
	}
	column := ""
	switch q.GroupBy {
	case "":
	case "group":
		column = "group_name"
	case "model":
		column = "model_id"
	default:
		return nil, errors.New("invalid attempt grouping")
	}
	where := []string{"1=1"}
	args := []any{}
	if !q.From.IsZero() {
		where = append(where, "started_at >= ?")
		args = append(args, formatTimestamp(q.From))
	}
	if !q.To.IsZero() {
		where = append(where, "started_at <= ?")
		args = append(args, formatTimestamp(q.To))
	}
	selectGroup := ""
	if column != "" {
		selectGroup = column + ", "
	}
	rows, err := l.db.QueryContext(ctx, `SELECT `+selectGroup+`count(*), count(input_tokens), count(output_tokens), count(total_tokens), sum(input_tokens), sum(output_tokens), sum(total_tokens) FROM routing_attempts WHERE `+strings.Join(where, " AND ")+func() string {
		if column != "" {
			return " GROUP BY " + column + " ORDER BY count(*) DESC, " + column + " ASC LIMIT 200"
		}
		return ""
	}(), args...)
	if err != nil {
		return nil, fmt.Errorf("aggregate routing attempts: %w", err)
	}
	defer rows.Close()
	out := make([]AttemptStats, 0, 8)
	for rows.Next() {
		var s AttemptStats
		var label sql.NullString
		var in, outTokens, total sql.NullInt64
		dest := []any{&s.Count, &s.InputObserved, &s.OutputObserved, &s.TotalObserved, &in, &outTokens, &total}
		if column != "" {
			dest = append([]any{&label}, dest...)
		}
		if err := rows.Scan(dest...); err != nil {
			return nil, fmt.Errorf("scan attempt aggregate: %w", err)
		}
		s.Group = label.String
		s.InputTokens = optionalInt64(in)
		s.OutputTokens = optionalInt64(outTokens)
		s.TotalTokens = optionalInt64(total)
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read attempt aggregate: %w", err)
	}
	return out, nil
}

// OutputTokensPerSecond returns the output-token sum for attempts completed in
// [now-60s, now], divided by exactly 60 seconds. Unknown output counts are omitted.
func (l *RoutingLog) OutputTokensPerSecond(ctx context.Context, now time.Time) (*float64, error) {
	if l.db == nil {
		return nil, errors.New("the routing log has no database")
	}
	var tokens sql.NullInt64
	err := l.db.QueryRowContext(ctx, `SELECT sum(output_tokens) FROM routing_attempts WHERE completed_at >= ? AND completed_at <= ? AND output_tokens IS NOT NULL`, formatTimestamp(now.Add(-time.Minute)), formatTimestamp(now)).Scan(&tokens)
	if err != nil {
		return nil, fmt.Errorf("aggregate recent attempt output tokens: %w", err)
	}
	if !tokens.Valid {
		return nil, nil
	}
	value := float64(tokens.Int64) / 60
	return &value, nil
}

// DeleteEventsBefore removes at most limit routing events older than cutoff. The
// portable "DELETE ... WHERE id IN (SELECT ... LIMIT ?)" form is used instead of
// the optional UPDATE_DELETE_LIMIT build flag, so the same statement works on
// every SQLite this project can be built against.
func (l *RoutingLog) DeleteEventsBefore(ctx context.Context, cutoff time.Time, limit int) (int, error) {
	return l.deleteBefore(ctx, "routing_events", cutoff, limit)
}

// DeleteJevCallsBefore removes at most limit Jev traces older than cutoff.
func (l *RoutingLog) DeleteJevCallsBefore(ctx context.Context, cutoff time.Time, limit int) (int, error) {
	return l.deleteBefore(ctx, "routing_jev_calls", cutoff, limit)
}

func (l *RoutingLog) deleteBefore(ctx context.Context, table string, cutoff time.Time, limit int) (int, error) {
	if l.db == nil {
		return 0, errors.New("the routing log has no database")
	}
	if limit <= 0 {
		return 0, errors.New("the retention batch size must be positive")
	}
	// The table name is one of two compile-time constants; it never comes from
	// input, so the concatenation cannot carry an injection. The subquery walks
	// (started_at, id), which is the index the migration created for it.
	result, err := l.db.ExecContext(ctx, fmt.Sprintf(
		"DELETE FROM %s WHERE id IN (SELECT id FROM %s WHERE started_at < ? ORDER BY started_at, id LIMIT ?)", table, table),
		formatTimestamp(cutoff), limit)
	if err != nil {
		return 0, fmt.Errorf("expire %s: %w", table, err)
	}
	deleted, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("count expired %s rows: %w", table, err)
	}
	return int(deleted), nil
}

// storedRow is one event broken into the value lists the two statements need. It
// exists so the nullable columns are converted in exactly one place: a token count
// that was never observed must become NULL, and NULL must never become zero.
type storedRow struct {
	requestID string
	startedAt string
	values    []any
	trace     *storedTrace
}

// storedTrace is the trace part of an event in column order.
type storedTrace struct {
	status         string
	failureReason  string
	inputMode      string
	latencyMS      *int64
	candidateCount int
	models         []string
	modelCount     int
	selected       string
	confidence     *float64
	confidenceBand string
	fallbackReason string
	probabilities  []logging.ModelProbability
	evidenceHash   string
}

// values renders the trace row, replacing an oversized diagnostic field with an
// empty list. The bound is enforced by the writer as well; this path exists so a
// direct caller of the store cannot write an unbounded blob into a diagnostic
// column.
func (t *storedTrace) values(requestID, startedAt string, logger *slog.Logger) []any {
	candidates, ok := logging.EncodeCandidateModels(t.models)
	if !ok {
		rejectDiagnosticField(logger, requestID, "candidates_json")
		candidates = "[]"
	}
	distribution, ok := logging.EncodeDistribution(t.probabilities)
	if !ok {
		rejectDiagnosticField(logger, requestID, "distribution_json")
		distribution = "[]"
	}
	return []any{
		requestID, startedAt, t.status, nullString(t.failureReason), t.inputMode, nullInt64(t.latencyMS),
		t.candidateCount, candidates, t.modelCount, nullString(t.selected), nullFloat64(t.confidence),
		nullString(t.confidenceBand), nullString(t.fallbackReason), distribution, nullString(t.evidenceHash),
	}
}

func rejectDiagnosticField(logger *slog.Logger, requestID, field string) {
	if logger == nil {
		return
	}
	logger.Warn("a routing log diagnostic field exceeded its bound and was replaced",
		"request_id", requestID, "field", field, "limit_bytes", logging.MaxJevJSONBytes)
}

// newStoredRow converts one event into its stored values.
func newStoredRow(event logging.Event) storedRow {
	row := storedRow{
		requestID: event.RequestID,
		startedAt: formatTimestamp(event.StartedAt),
		values: []any{
			event.RequestID,
			formatTimestamp(event.StartedAt),
			event.DurationMS,
			event.Protocol,
			nullString(event.RoutingMode),
			nullString(event.SelectionMode),
			nullString(event.RequestedModel),
			nullString(event.ProviderKey),
			nullString(event.UpstreamModel),
			event.Status,
			nullStatus(event.UpstreamStatus),
			nullString(event.ErrorCode),
			boolInt(event.Stream),
			event.BytesWritten,
			nullString(event.ClientIP),
			nullString(event.RoutingPreference),
			nullString(event.PreferenceSource),
			nullString(event.JevStatus),
			nullFloat64(event.Confidence),
			nullString(event.ConfidenceBand),
			nullString(event.FallbackReason),
			nullString(event.EvidenceHash),
			event.GatewayAttempts,
			boolInt(event.FailoverUsed),
			nullInt64(event.RoutingLatencyMS),
			nullInt64(event.JevLatencyMS),
			nullInt64(event.Usage.InputTokens),
			nullInt64(event.Usage.OutputTokens),
			nullInt64(event.Usage.TotalTokens),
			event.Usage.Status,
			nullString(event.Usage.Source),
			// The two logical model identifiers are appended rather than inserted so
			// the order of the arguments above still matches the order the columns were
			// created in, which is what keeps a future column from silently shifting
			// every value by one.
			nullString(event.EffectiveModel),
			nullString(event.JevTopModel),
		},
	}
	if event.Jev != nil {
		row.trace = &storedTrace{
			status:         event.Jev.Status,
			failureReason:  event.Jev.FailureReason,
			inputMode:      event.Jev.InputMode,
			latencyMS:      event.Jev.LatencyMS,
			candidateCount: event.Jev.CandidateCount,
			models:         event.Jev.CandidateModels,
			modelCount:     event.Jev.ModelCount,
			selected:       event.Jev.Selected,
			confidence:     event.Jev.Confidence,
			confidenceBand: event.Jev.ConfidenceBand,
			fallbackReason: event.Jev.FallbackReason,
			probabilities:  event.Jev.Probabilities,
			evidenceHash:   event.Jev.EvidenceHash,
		}
	}
	return row
}

// nullString maps an empty string onto NULL. No identifier in this schema has an
// empty value that means something, so "" and "absent" are the same fact.
func nullString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

// nullStatus maps the "no upstream call" zero onto NULL. An HTTP status is never
// legitimately zero.
func nullStatus(value int) any {
	if value == 0 {
		return nil
	}
	return value
}

func nullInt64(value *int64) any {
	if value == nil {
		return nil
	}
	return *value
}

func nullFloat64(value *float64) any {
	if value == nil {
		return nil
	}
	return *value
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

// StoredEvent is one routing event as it was written back: the row identifier, the
// event itself and the Jev trace that shares its request identifier.
type StoredEvent struct {
	ID        int64
	Event     logging.Event
	JevCallID *int64
	JevTrace  *logging.JevTrace
}

// SelectRecentEvents returns the most recent events, newest first, each with the
// Jev trace that shares its request identifier (nil when no trace was written).
// The limit is applied in SQL and is the caller's bound.
func (l *RoutingLog) SelectRecentEvents(ctx context.Context, limit int) ([]StoredEvent, error) {
	if l.db == nil {
		return nil, errors.New("the routing log has no database")
	}
	if limit <= 0 {
		return nil, errors.New("the log query limit must be positive")
	}
	rows, err := l.db.QueryContext(ctx, `SELECT id, `+routingEventColumns+` FROM routing_events ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("read routing events: %w", err)
	}
	defer rows.Close()
	stored := make([]StoredEvent, 0, limit)
	for rows.Next() {
		entry, err := scanStoredEvent(rows)
		if err != nil {
			return nil, err
		}
		stored = append(stored, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read routing events: %w", err)
	}
	// The traces are fetched per event so the printed output is exactly the row
	// that shares the request identifier: an event without a trace stays without
	// one, and a trace is never reconstructed from the event's own fields.
	for i := range stored {
		trace, id, err := l.selectTraceFor(ctx, stored[i].Event.RequestID)
		if err != nil {
			return nil, err
		}
		stored[i].JevTrace, stored[i].JevCallID = trace, id
	}
	return stored, nil
}

// scanner is the subset of *sql.Rows and *sql.Row the event scan needs.
type scanner interface {
	Scan(dest ...any) error
}

// scanStoredEvent reads one row of routing_events in routingEventColumns order.
func scanStoredEvent(row scanner) (StoredEvent, error) {
	var (
		entry            StoredEvent
		startedAt        string
		routingMode      sql.NullString
		selectionMode    sql.NullString
		requestedModel   sql.NullString
		providerKey      sql.NullString
		upstreamModel    sql.NullString
		upstreamStatus   sql.NullInt64
		errorCode        sql.NullString
		stream           int
		clientIP         sql.NullString
		preference       sql.NullString
		preferenceSource sql.NullString
		jevStatus        sql.NullString
		confidence       sql.NullFloat64
		band             sql.NullString
		fallbackReason   sql.NullString
		evidenceHash     sql.NullString
		failoverUsed     int
		routingLatency   sql.NullInt64
		jevLatency       sql.NullInt64
		inputTokens      sql.NullInt64
		outputTokens     sql.NullInt64
		totalTokens      sql.NullInt64
		usageSource      sql.NullString
		effectiveModel   sql.NullString
		jevTopModel      sql.NullString
	)
	if err := row.Scan(
		&entry.ID, &entry.Event.RequestID, &startedAt, &entry.Event.DurationMS, &entry.Event.Protocol,
		&routingMode, &selectionMode, &requestedModel, &providerKey, &upstreamModel,
		&entry.Event.Status, &upstreamStatus, &errorCode, &stream, &entry.Event.BytesWritten,
		&clientIP, &preference, &preferenceSource, &jevStatus, &confidence, &band, &fallbackReason,
		&evidenceHash, &entry.Event.GatewayAttempts, &failoverUsed, &routingLatency, &jevLatency,
		&inputTokens, &outputTokens, &totalTokens, &entry.Event.Usage.Status, &usageSource,
		&effectiveModel, &jevTopModel,
	); err != nil {
		return StoredEvent{}, fmt.Errorf("scan routing event: %w", err)
	}
	parsed, err := time.Parse(time.RFC3339Nano, startedAt)
	if err != nil {
		return StoredEvent{}, fmt.Errorf("parse stored start time: %w", err)
	}
	entry.Event.StartedAt = parsed
	entry.Event.RoutingMode = routingMode.String
	entry.Event.SelectionMode = selectionMode.String
	entry.Event.RequestedModel = requestedModel.String
	entry.Event.ProviderKey = providerKey.String
	entry.Event.UpstreamModel = upstreamModel.String
	if upstreamStatus.Valid {
		entry.Event.UpstreamStatus = int(upstreamStatus.Int64)
	}
	entry.Event.ErrorCode = errorCode.String
	entry.Event.Stream = stream == 1
	entry.Event.ClientIP = clientIP.String
	entry.Event.RoutingPreference = preference.String
	entry.Event.PreferenceSource = preferenceSource.String
	entry.Event.JevStatus = jevStatus.String
	if confidence.Valid {
		value := confidence.Float64
		entry.Event.Confidence = &value
	}
	entry.Event.ConfidenceBand = band.String
	entry.Event.FallbackReason = fallbackReason.String
	entry.Event.EvidenceHash = evidenceHash.String
	entry.Event.FailoverUsed = failoverUsed == 1
	entry.Event.RoutingLatencyMS = optionalInt64(routingLatency)
	entry.Event.JevLatencyMS = optionalInt64(jevLatency)
	entry.Event.Usage.InputTokens = optionalInt64(inputTokens)
	entry.Event.Usage.OutputTokens = optionalInt64(outputTokens)
	entry.Event.Usage.TotalTokens = optionalInt64(totalTokens)
	entry.Event.Usage.Source = usageSource.String
	entry.Event.EffectiveModel = effectiveModel.String
	entry.Event.JevTopModel = jevTopModel.String
	return entry, nil
}

// selectTraceFor reads the newest trace for one request identifier. A request can
// only ever produce one, so the ordering is a deterministic tie-break rather than
// a selection rule.
func (l *RoutingLog) selectTraceFor(ctx context.Context, requestID string) (*logging.JevTrace, *int64, error) {
	var (
		id             int64
		trace          logging.JevTrace
		failureReason  sql.NullString
		latencyMS      sql.NullInt64
		candidatesJSON string
		selected       sql.NullString
		confidence     sql.NullFloat64
		band           sql.NullString
		fallbackReason sql.NullString
		distribution   string
		evidenceHash   sql.NullString
	)
	err := l.db.QueryRowContext(ctx, `SELECT id, status, failure_reason, input_mode, latency_ms, candidate_count,
		candidates_json, model_count, selected_model, confidence, confidence_band, fallback_reason,
		distribution_json, evidence_hash FROM routing_jev_calls WHERE request_id = ? ORDER BY id DESC LIMIT 1`, requestID).Scan(
		&id, &trace.Status, &failureReason, &trace.InputMode, &latencyMS, &trace.CandidateCount,
		&candidatesJSON, &trace.ModelCount, &selected, &confidence, &band, &fallbackReason,
		&distribution, &evidenceHash)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, fmt.Errorf("read routing trace for %q: %w", requestID, err)
	}
	trace.FailureReason = failureReason.String
	trace.LatencyMS = optionalInt64(latencyMS)
	trace.Selected = selected.String
	if confidence.Valid {
		value := confidence.Float64
		trace.Confidence = &value
	}
	trace.ConfidenceBand = band.String
	trace.FallbackReason = fallbackReason.String
	trace.EvidenceHash = evidenceHash.String
	if err := json.Unmarshal([]byte(candidatesJSON), &trace.CandidateModels); err != nil {
		return nil, nil, fmt.Errorf("decode stored candidate models: %w", err)
	}
	if trace.CandidateModels == nil {
		trace.CandidateModels = []string{}
	}
	var probabilities []struct {
		Model       string  `json:"model"`
		Probability float64 `json:"probability"`
	}
	if err := json.Unmarshal([]byte(distribution), &probabilities); err != nil {
		return nil, nil, fmt.Errorf("decode stored distribution: %w", err)
	}
	trace.Probabilities = make([]logging.ModelProbability, 0, len(probabilities))
	for _, probability := range probabilities {
		trace.Probabilities = append(trace.Probabilities, logging.ModelProbability{
			Model:       probability.Model,
			Probability: probability.Probability,
		})
	}
	return &trace, &id, nil
}

func optionalInt64(value sql.NullInt64) *int64 {
	if !value.Valid {
		return nil
	}
	copied := value.Int64
	return &copied
}
