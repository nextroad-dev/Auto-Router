package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/nextroad-dev/Auto-Router/internal/logging"
)

// This file is the routing log's read side: the paginated event query and the
// aggregate query the Admin API serves.
//
// Two properties are structural rather than promised:
//
//   - the event query selects routingEventColumns, the same constant the writer
//     inserts and the -logs-check mode reads. There is no second column list, so
//     a new column cannot appear in one place and be forgotten in another;
//   - every predicate is built from a closed set the caller validated through
//     internal/logging's own validators, and every identifier is bound as a
//     parameter. The only string concatenation is the ORDER BY direction, which is
//     chosen from two literals.

// EventFilter narrows the paginated event query. Every field is optional. The
// closed-set fields are validated by the caller through the logging package's
// validators before a filter reaches this layer; the free-form fields (request_id,
// provider, model) are bound as parameters and never concatenated.
type EventFilter struct {
	ProviderKey    string
	Model          string
	Protocol       string
	RoutingMode    string
	SelectionMode  string
	Status         int
	StatusClass    string
	ErrorCode      string
	JevStatus      string
	ConfidenceBand string
	FallbackReason string
	UsageStatus    string
	RequestID      string
	// Search matches request ID, provider, requested model, and effective model as
	// a literal substring. Empty leaves the full listing unchanged.
	Search string
	// Stream is nil when the filter does not care.
	Stream *bool
	// From and To are inclusive bounds on started_at. Zero means unbounded.
	From time.Time
	To   time.Time
}

// EventPage is one page of stored events.
type EventPage struct {
	Events []StoredEvent
	// More reports whether another page exists, which is what the cursor is built
	// from. It is computed by asking for one row more than the page size.
	More bool
}

// SelectEvents returns one page of events, newest first, using the
// (started_at, id) index for the ordering and the keyset comparison.
//
// The cursor is the (started_at, id) pair of the last row of the previous page.
// A zero cursor starts at the newest row. Keyset pagination is used rather than
// OFFSET because a concurrent insert shifts an OFFSET window and would make a row
// appear twice or not at all.
func (l *RoutingLog) SelectEvents(ctx context.Context, filter EventFilter, before EventCursor, limit int) (EventPage, error) {
	if l.db == nil {
		return EventPage{}, errors.New("the routing log has no database")
	}
	if limit <= 0 || limit > maxAdminQueryLimit {
		return EventPage{}, fmt.Errorf("the query limit must be between 1 and %d", maxAdminQueryLimit)
	}
	where, arguments := eventPredicates(filter)
	// The cursor's two values are appended after the filter's, so the filter
	// builder never has to know how many parameters it produced.
	cursorArguments := make([]any, 0, 2)
	if !before.StartedAt.IsZero() {
		where = append(where, "(started_at < ? OR (started_at = ? AND id < ?))")
		cursorArguments = append(cursorArguments, formatTimestamp(before.StartedAt), formatTimestamp(before.StartedAt), before.ID)
	}
	// The requested page size plus one row tells the caller whether more rows
	// exist without a second count query, so "next_cursor" is never null when
	// there is something to fetch.
	arguments = append(append(arguments, cursorArguments...), limit+1)
	query := `SELECT id, ` + routingEventColumns + ` FROM routing_events WHERE ` + strings.Join(where, " AND ") + `
		ORDER BY started_at DESC, id DESC LIMIT ?`
	rows, err := l.db.QueryContext(ctx, query, arguments...)
	if err != nil {
		return EventPage{}, fmt.Errorf("read routing events: %w", err)
	}
	defer rows.Close()
	events := make([]StoredEvent, 0, limit)
	for rows.Next() {
		entry, err := scanStoredEvent(rows)
		if err != nil {
			return EventPage{}, err
		}
		events = append(events, entry)
	}
	if err := rows.Err(); err != nil {
		return EventPage{}, fmt.Errorf("read routing events: %w", err)
	}
	page := EventPage{}
	if len(events) > limit {
		page.More = true
		events = events[:limit]
	}
	page.Events = events
	return page, nil
}

// EventCursor is the position of a paginated event listing: the sort key of the
// last row already returned.
type EventCursor struct {
	StartedAt time.Time
	ID        int64
}

// eventPredicates renders the filter as a WHERE clause and its bound arguments.
// The clause list always starts with "1 = 1" so the caller never has to handle an
// empty predicate set.
func eventPredicates(filter EventFilter) ([]string, []any) {
	where := []string{"1 = 1"}
	arguments := []any{}
	for _, equality := range []struct {
		column string
		value  string
	}{
		{"provider_key", filter.ProviderKey},
		{"requested_model", filter.Model},
		{"protocol", filter.Protocol},
		{"routing_mode", filter.RoutingMode},
		{"selection_mode", filter.SelectionMode},
		{"error_code", filter.ErrorCode},
		{"jev_status", filter.JevStatus},
		{"confidence_band", filter.ConfidenceBand},
		{"fallback_reason", filter.FallbackReason},
		{"usage_status", filter.UsageStatus},
		{"request_id", filter.RequestID},
	} {
		if equality.value == "" {
			continue
		}
		where = append(where, equality.column+" = ?")
		arguments = append(arguments, equality.value)
	}
	if filter.Search != "" {
		where = append(where, `(instr(lower(request_id), lower(?)) > 0 OR instr(lower(provider_key), lower(?)) > 0 OR instr(lower(requested_model), lower(?)) > 0 OR instr(lower(effective_model), lower(?)) > 0)`)
		arguments = append(arguments, filter.Search, filter.Search, filter.Search, filter.Search)
	}
	if filter.Status != 0 {
		where = append(where, "status = ?")
		arguments = append(arguments, filter.Status)
	}
	if filter.Stream != nil {
		where = append(where, "stream = ?")
		arguments = append(arguments, boolToInt(*filter.Stream))
	}
	switch filter.StatusClass {
	case StatusClassSuccess:
		where = append(where, "status BETWEEN 200 AND 299")
	case StatusClassClientError:
		where = append(where, "status BETWEEN 400 AND 499")
	case StatusClassServerError:
		where = append(where, "status BETWEEN 500 AND 599")
	}
	if !filter.From.IsZero() {
		where = append(where, "started_at >= ?")
		arguments = append(arguments, formatTimestamp(filter.From))
	}
	if !filter.To.IsZero() {
		where = append(where, "started_at <= ?")
		arguments = append(arguments, formatTimestamp(filter.To))
	}
	return where, arguments
}

// The status classes the log query accepts. They are a closed set: an unknown
// value would silently select an empty page.
const (
	StatusClassSuccess     = "success"
	StatusClassClientError = "client_error"
	StatusClassServerError = "server_error"
)

// ValidStatusClass reports whether a value is one of the three status classes.
func ValidStatusClass(value string) bool {
	switch value {
	case StatusClassSuccess, StatusClassClientError, StatusClassServerError:
		return true
	default:
		return false
	}
}

// EventStats is one group of the aggregate query. Every aggregate is reported as
// a pair of facts: the count it was computed over and the value itself. A token
// sum over zero observed rows is nil, not zero, because "nobody reported tokens"
// and "everybody reported zero tokens" are different findings.
type EventStats struct {
	// Group is the value of the grouped column, empty for a total.
	Group string
	// Count is the number of events in this group.
	Count int64
	// MeanDurationMS and P95DurationMS are computed over every event in the group.
	MeanDurationMS *float64
	P95DurationMS  *int64
	// InputTokens, OutputTokens and TotalTokens are sums over the rows that
	// observed usage, and ObservedUsageCount is how many rows those sums cover.
	// InputTokensObserved and friends report how many rows contributed to each
	// individual sum, because a row may report one count and not another.
	InputTokens         *int64
	OutputTokens        *int64
	TotalTokens         *int64
	ObservedUsageCount  int64
	InputTokensObserved int64
	OutputObserved      int64
	TotalTokensObserved int64
	// Statuses counts how many events in this group ended in each status class.
	// It is a fixed three-element structure so a report never depends on map
	// iteration for its shape.
	Success     int64
	ClientError int64
	ServerError int64
}

// StatsQuery is one aggregate request.
type StatsQuery struct {
	Filter EventFilter
	// GroupBy is one of the five groupable columns, or empty for a single total.
	GroupBy string
}

// The columns the aggregate query may group by. They are a closed set: the name
// is mapped onto a compile-time column list, so the concatenation cannot carry
// input.
const (
	GroupByModel         = "model"
	GroupByProvider      = "provider"
	GroupByRoutingMode   = "routing_mode"
	GroupBySelectionMode = "selection_mode"
	GroupByUsageStatus   = "usage_status"
	GroupByStatusClass   = "status_class"
	// GroupByEffectiveModel groups by the logical model that actually took effect,
	// which is a different question from GroupByModel's "what did the client ask
	// for". The distinction matters most on the automatic path, where the client
	// asks for "auto" every time: grouping by the requested model would collapse
	// every routed request into one bucket.
	GroupByEffectiveModel = "effective_model"
)

// GroupByValues returns the accepted group-by names in a fixed order.
func GroupByValues() []string {
	return []string{GroupByModel, GroupByProvider, GroupByRoutingMode, GroupBySelectionMode, GroupByUsageStatus, GroupByStatusClass, GroupByEffectiveModel}
}

// ValidGroupBy reports whether name is one of the seven group-by values.
func ValidGroupBy(name string) bool {
	switch name {
	case GroupByModel, GroupByProvider, GroupByRoutingMode, GroupBySelectionMode, GroupByUsageStatus, GroupByStatusClass, GroupByEffectiveModel:
		return true
	default:
		return false
	}
}

// Stats computes the aggregate report. The result is ordered deterministically
// (descending count, then group ascending) and bounded by maxStatsGroups, so a
// high-cardinality column cannot make one request allocate without limit.
func (l *RoutingLog) Stats(ctx context.Context, query StatsQuery) ([]EventStats, error) {
	if l.db == nil {
		return nil, errors.New("the routing log has no database")
	}
	group, groupSelect := "", ""
	groupOrder := ""
	switch query.GroupBy {
	case "":
		// A single total needs no GROUP BY at all.
	case GroupByModel:
		group, groupSelect, groupOrder = "requested_model", "requested_model", "requested_model"
	case GroupByProvider:
		group, groupSelect, groupOrder = "provider_key", "provider_key", "provider_key"
	case GroupByRoutingMode:
		group, groupSelect, groupOrder = "routing_mode", "routing_mode", "routing_mode"
	case GroupBySelectionMode:
		group, groupSelect, groupOrder = "selection_mode", "selection_mode", "selection_mode"
	case GroupByUsageStatus:
		group, groupSelect, groupOrder = "usage_status", "usage_status", "usage_status"
	case GroupByStatusClass:
		// The status class is derived, not stored: the grouping expression is a
		// CASE over the status column, and its result is the group label.
		group = "CASE WHEN status BETWEEN 200 AND 299 THEN 'success' WHEN status BETWEEN 400 AND 499 THEN 'client_error' ELSE 'server_error' END"
		groupSelect = group + " AS grp"
		groupOrder = "grp"
	case GroupByEffectiveModel:
		group, groupSelect, groupOrder = "effective_model", "effective_model", "effective_model"
	}
	where, arguments := eventPredicates(query.Filter)
	selected := `count(*) AS events,
		avg(duration_ms) AS mean_duration
		, count(input_tokens) AS input_observed
		, count(output_tokens) AS output_observed
		, count(total_tokens) AS total_observed
		, sum(input_tokens) AS input_tokens
		, sum(output_tokens) AS output_tokens
		, sum(total_tokens) AS total_tokens
		, coalesce(sum(CASE WHEN usage_status = 'observed' THEN 1 ELSE 0 END), 0) AS observed_usage
		, coalesce(sum(CASE WHEN status BETWEEN 200 AND 299 THEN 1 ELSE 0 END), 0) AS success
		, coalesce(sum(CASE WHEN status BETWEEN 400 AND 499 THEN 1 ELSE 0 END), 0) AS client_error
		, coalesce(sum(CASE WHEN status BETWEEN 500 AND 599 THEN 1 ELSE 0 END), 0) AS server_error`
	if groupSelect != "" {
		selected = groupSelect + ", " + selected
	}
	statement := `SELECT ` + selected + ` FROM routing_events WHERE ` + strings.Join(where, " AND ")
	if group != "" {
		statement += " GROUP BY " + group
		// The bound is applied after grouping, so a high-cardinality column cannot
		// make one request materialize an unbounded result.
		statement += fmt.Sprintf(" ORDER BY events DESC, %s ASC LIMIT %d", groupOrder, maxStatsGroups)
	}
	rows, err := l.db.QueryContext(ctx, statement, arguments...)
	if err != nil {
		return nil, fmt.Errorf("aggregate routing events: %w", err)
	}
	defer rows.Close()
	groups := make([]EventStats, 0, 8)
	for rows.Next() {
		entry := EventStats{}
		var label sql.NullString
		var mean sql.NullFloat64
		var input, output, total sql.NullInt64
		destinations := []any{&entry.Count, &mean, &entry.InputTokensObserved, &entry.OutputObserved, &entry.TotalTokensObserved,
			&input, &output, &total, &entry.ObservedUsageCount, &entry.Success, &entry.ClientError, &entry.ServerError}
		if groupSelect != "" {
			destinations = append([]any{&label}, destinations...)
		}
		if err := rows.Scan(destinations...); err != nil {
			return nil, fmt.Errorf("scan usage aggregate: %w", err)
		}
		if groupSelect != "" {
			// The label is read after the scan, which is the only order that reads the
			// value the row actually carried.
			entry.Group = label.String
		}
		if mean.Valid {
			value := mean.Float64
			entry.MeanDurationMS = &value
		}
		entry.InputTokens = optionalInt64(input)
		entry.OutputTokens = optionalInt64(output)
		entry.TotalTokens = optionalInt64(total)
		groups = append(groups, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("aggregate routing events: %w", err)
	}
	// The p95 needs the individual durations, so it is computed per group from a
	// bounded windowed read rather than from a percentile extension SQLite does not
	// have. The group list is already bounded by maxStatsGroups.
	for i := range groups {
		p95, err := l.percentileDuration(ctx, query.Filter, query.GroupBy, groups[i].Group)
		if err != nil {
			return nil, err
		}
		groups[i].P95DurationMS = p95
	}
	return groups, nil
}

// maxStatsGroups bounds how many groups one aggregate request may return.
const maxStatsGroups = 200

// percentileDuration computes the nearest-rank p95 of one group's durations. It
// reads at most maxStatsSamples rows, which keeps one aggregate request bounded
// even on a large log; the count the caller reports still comes from the exact
// aggregate query, so the sample bound never changes a count.
func (l *RoutingLog) percentileDuration(ctx context.Context, filter EventFilter, groupBy, group string) (*int64, error) {
	where, arguments := eventPredicates(filter)
	switch groupBy {
	case "":
	case GroupByModel:
		where = append(where, "requested_model IS ?")
		arguments = append(arguments, nullString(group))
	case GroupByProvider:
		where = append(where, "provider_key IS ?")
		arguments = append(arguments, nullString(group))
	case GroupByRoutingMode:
		where = append(where, "routing_mode IS ?")
		arguments = append(arguments, nullString(group))
	case GroupBySelectionMode:
		where = append(where, "selection_mode IS ?")
		arguments = append(arguments, nullString(group))
	case GroupByUsageStatus:
		where = append(where, "usage_status = ?")
		arguments = append(arguments, group)
	case GroupByEffectiveModel:
		// The grouping column is nullable, so the comparison uses IS: a NULL group
		// label means "no model took effect", and "= NULL" would match nothing.
		where = append(where, "effective_model IS ?")
		arguments = append(arguments, nullString(group))
	case GroupByStatusClass:
		switch group {
		case StatusClassSuccess:
			where = append(where, "status BETWEEN 200 AND 299")
		case StatusClassClientError:
			where = append(where, "status BETWEEN 400 AND 499")
		case StatusClassServerError:
			where = append(where, "status BETWEEN 500 AND 599")
		default:
			// A group label that is not one of the three classes cannot have come
			// from the CASE expression, so there is nothing to compute.
			return nil, nil
		}
	}
	arguments = append(arguments, maxStatsSamples)
	rows, err := l.db.QueryContext(ctx, `SELECT duration_ms FROM routing_events WHERE `+strings.Join(where, " AND ")+` ORDER BY duration_ms LIMIT ?`, arguments...)
	if err != nil {
		return nil, fmt.Errorf("read durations: %w", err)
	}
	defer rows.Close()
	durations := make([]int64, 0, 64)
	for rows.Next() {
		var duration int64
		if err := rows.Scan(&duration); err != nil {
			return nil, fmt.Errorf("scan duration: %w", err)
		}
		durations = append(durations, duration)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read durations: %w", err)
	}
	if len(durations) == 0 {
		return nil, nil
	}
	// Nearest rank: the smallest value at or above the 95th percentile of the
	// sample. It is computed on the ascending list, so no sort is needed here.
	index := (95*len(durations) + 99) / 100
	if index > 0 {
		index--
	}
	value := durations[index]
	return &value, nil
}

// maxStatsSamples bounds how many durations one percentile computation reads.
const maxStatsSamples = 10000

// CountEvents returns how many events match a filter. It exists for the Admin API
// health report, which must not read rows to count them.
func (l *RoutingLog) CountEvents(ctx context.Context, filter EventFilter) (int64, error) {
	if l.db == nil {
		return 0, errors.New("the routing log has no database")
	}
	where, arguments := eventPredicates(filter)
	var count int64
	if err := l.db.QueryRowContext(ctx, `SELECT count(*) FROM routing_events WHERE `+strings.Join(where, " AND "), arguments...).Scan(&count); err != nil {
		return 0, fmt.Errorf("count routing events: %w", err)
	}
	return count, nil
}

// CountJevCalls returns how many Jev traces are stored.
func (l *RoutingLog) CountJevCalls(ctx context.Context) (int64, error) {
	if l.db == nil {
		return 0, errors.New("the routing log has no database")
	}
	var count int64
	if err := l.db.QueryRowContext(ctx, `SELECT count(*) FROM routing_jev_calls`).Scan(&count); err != nil {
		return 0, fmt.Errorf("count jev calls: %w", err)
	}
	return count, nil
}

// CountJevOK returns how many automatic requests in the window have a successful
// Jev call. The predicate is the stored jev_status value, so this count and the
// jev_invocation_rate numerator are the same fact.
func (l *RoutingLog) CountJevOK(ctx context.Context, from, to time.Time) (int64, error) {
	return l.countAutomatic(ctx, from, to, "jev_status = ?", logging.JevStatusOK)
}

// CountJevAdopted returns how many automatic requests in the window were routed
// to the group their successful Jev call recommended: jev_status is ok and no
// fallback reason was recorded. It is the jev_group_adoption_rate numerator; the
// denominator is CountJevOK over the same window, so the numerator is a subset
// of the denominator by construction.
func (l *RoutingLog) CountJevAdopted(ctx context.Context, from, to time.Time) (int64, error) {
	return l.countAutomatic(ctx, from, to, "jev_status = ? AND fallback_reason = ?", logging.JevStatusOK, logging.FallbackReasonNone)
}

// countAutomatic counts automatic requests in one window that satisfy one extra
// predicate. The predicate is one of three compile-time constants; it never comes
// from input, so the concatenation cannot carry an injection.
func (l *RoutingLog) countAutomatic(ctx context.Context, from, to time.Time, predicate string, arguments ...any) (int64, error) {
	if l.db == nil {
		return 0, errors.New("the routing log has no database")
	}
	where, window := windowPredicates(from, to)
	where = append(where, "routing_mode = ?", predicate)
	bound := append(append([]any{}, window...), logging.RoutingModeAuto)
	bound = append(bound, arguments...)
	var count int64
	if err := l.db.QueryRowContext(ctx, `SELECT count(*) FROM routing_events WHERE `+strings.Join(where, " AND "), bound...).Scan(&count); err != nil {
		return 0, fmt.Errorf("count routing events: %w", err)
	}
	return count, nil
}

// SelectTraceFor reads the optional Jev trace that shares a request identifier.
// It is the same query the -logs-check mode uses, exposed so the Admin API's
// request_id filter returns the trace with the event it belongs to rather than
// requiring a second, differently shaped detail endpoint.
func (l *RoutingLog) SelectTraceFor(ctx context.Context, requestID string) (*logging.JevTrace, error) {
	trace, _, err := l.selectTraceFor(ctx, requestID)
	return trace, err
}

// ModelScore is one logical model as the scoreboard reports it: how many
// automatic requests actually took effect on it.
type ModelScore struct {
	Model     string
	Decisions int64
}

// ModelScoreboard reports, per logical model, how many automatic decisions took
// effect on it in the window.
func (l *RoutingLog) ModelScoreboard(ctx context.Context, from, to time.Time, limit int) ([]ModelScore, error) {
	if l.db == nil {
		return nil, errors.New("the routing log has no database")
	}
	if limit <= 0 || limit > maxStatsGroups {
		return nil, fmt.Errorf("the scoreboard limit must be between 1 and %d", maxStatsGroups)
	}
	window, arguments := windowPredicates(from, to)
	// An explicit request made no automatic decision, so it is not scored.
	predicates := append(append([]string{}, window...), "routing_mode = ?")
	scored := append(append([]any{}, arguments...), logging.RoutingModeAuto)

	scores := make(map[string]*ModelScore)
	decisions, err := l.db.QueryContext(ctx, `SELECT effective_model, count(*)
		FROM routing_events WHERE `+strings.Join(predicates, " AND ")+`
		GROUP BY effective_model`, scored...)
	if err != nil {
		return nil, fmt.Errorf("aggregate decisions by model: %w", err)
	}
	defer decisions.Close()
	for decisions.Next() {
		var model sql.NullString
		var count int64
		if err := decisions.Scan(&model, &count); err != nil {
			return nil, fmt.Errorf("scan model decision count: %w", err)
		}
		// A NULL effective_model is not a model, so it is not a scoreboard row: the
		// requests it covers failed before a destination resolved, which the rate
		// endpoint reports through the auto_decision_success_rate denominator.
		if !model.Valid {
			continue
		}
		scores[model.String] = &ModelScore{Model: model.String, Decisions: count}
	}
	if err := decisions.Err(); err != nil {
		return nil, fmt.Errorf("read model decisions: %w", err)
	}

	ordered := make([]ModelScore, 0, len(scores))
	for _, score := range scores {
		ordered = append(ordered, *score)
	}
	// The order is total and independent of map iteration: most decisions first,
	// then the identifier ascending. It is what makes the response stable across
	// identical requests, which a caller comparing two reports depends on.
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].Decisions != ordered[j].Decisions {
			return ordered[i].Decisions > ordered[j].Decisions
		}
		return ordered[i].Model < ordered[j].Model
	})
	if len(ordered) > limit {
		ordered = ordered[:limit]
	}
	return ordered, nil
}

// windowPredicates renders the inclusive [from, to] window as predicates and bound
// arguments. A zero bound is unbounded, which is the same convention EventFilter
// uses.
func windowPredicates(from, to time.Time) ([]string, []any) {
	where := []string{"1 = 1"}
	arguments := []any{}
	if !from.IsZero() {
		where = append(where, "started_at >= ?")
		arguments = append(arguments, formatTimestamp(from))
	}
	if !to.IsZero() {
		where = append(where, "started_at <= ?")
		arguments = append(arguments, formatTimestamp(to))
	}
	return where, arguments
}
