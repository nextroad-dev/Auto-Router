package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/nextroad-dev/Auto-Router/internal/config"
	"github.com/nextroad-dev/Auto-Router/internal/logging"
	"github.com/nextroad-dev/Auto-Router/internal/storage"
)

// This file implements the -logs-check maintenance mode: an offline, read-only
// report of what the durable routing log contains.
//
// The mode is a proof tool. It opens the database read-only in the sense that it
// never writes a row, never runs the retention sweep and never starts the HTTP
// server, a Provider executor or a Jev client. It prints identifiers, enumerations,
// counts, durations and the observed token counts — never a prompt, a request body,
// a tool schema or a credential, none of which the routing log can contain in the
// first place.
//
// A missing database is an error rather than an empty report: a check that
// silently creates the file it was asked to inspect would answer every question
// with "nothing was logged".

// The report's bounds. The upper bound keeps a maintenance report readable and the
// query bounded; the lower bound keeps "show me the log" from being a no-op.
const (
	logsCheckDefaultLimit = 20
	logsCheckMinLimit     = 1
	logsCheckMaxLimit     = 200
)

// logsCheckFlags collects the flags that belong to the check.
type logsCheckFlags struct {
	enabled bool
	limit   int
	json    bool
}

func registerLogsCheckFlags(flags *flag.FlagSet, check *logsCheckFlags) {
	flags.BoolVar(&check.enabled, "logs-check", false, "read back the most recent routing log entries, then exit")
	flags.IntVar(&check.limit, "logs-limit", logsCheckDefaultLimit, "how many entries -logs-check prints (1..200)")
	flags.BoolVar(&check.json, "logs-json", false, "print one JSON object per entry instead of the text report")
}

// validate checks the check's own arguments. It runs after flag parsing and before
// configuration loading, so a typo fails fast without touching the database.
func (c logsCheckFlags) validate(explicitlySet map[string]bool) error {
	// The subordinate flags are meaningless on their own; accepting them would
	// silently do nothing, which is worse than refusing them.
	for _, name := range []string{"logs-limit", "logs-json"} {
		if explicitlySet[name] && !c.enabled {
			return fmt.Errorf("-%s requires -logs-check", name)
		}
	}
	if !c.enabled {
		return nil
	}
	if c.limit < logsCheckMinLimit || c.limit > logsCheckMaxLimit {
		return fmt.Errorf("-logs-limit must be between %d and %d", logsCheckMinLimit, logsCheckMaxLimit)
	}
	return nil
}

// runLogsCheck reads the routing log back and prints it. It is called after the
// database is open and migrated (so a database written by this binary is readable)
// and before the registry, the Provider executors, the Jev client and the listener exist.
func runLogsCheck(ctx context.Context, cfg config.Config, check logsCheckFlags, logger *slog.Logger, output io.Writer) error {
	if err := logsDatabaseExists(cfg.Database.Path); err != nil {
		return err
	}
	db, err := storage.Open(ctx, storage.Options{
		Path:        cfg.Database.Path,
		BusyTimeout: time.Duration(cfg.Database.BusyTimeout),
	})
	if err != nil {
		return err
	}
	defer db.Close()
	// Migration is idempotent and only ever adds the tables this binary owns. It is
	// run so a database created by an earlier stage is upgraded before the report,
	// which is exactly what starting the service would do.
	if err := storage.Migrate(ctx, db); err != nil {
		return err
	}
	store := storage.NewRoutingLog(db)
	events, err := store.SelectRecentEvents(ctx, check.limit)
	if err != nil {
		return err
	}
	// The report is written first so it is the first line of the output, whichever
	// format was requested: the process log line that follows is an operator's
	// confirmation that the mode ran offline, not part of the report.
	if check.json {
		if err := writeLogsJSON(output, events); err != nil {
			return err
		}
	} else {
		writeLogsReport(output, events)
	}
	if logger != nil {
		logger.Info("routing log check completed", "entries", len(events), "path", cfg.Database.Path)
	}
	return nil
}

// logsDatabaseExists refuses to inspect a database that is not there. It is
// checked before the connection is opened, because opening would create the file
// and turn "there is no log" into "the log is empty".
func logsDatabaseExists(path string) error {
	if path == "" || path == ":memory:" {
		return fmt.Errorf("no routing log database at %s", path)
	}
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("no routing log database at %s", path)
	}
	if info.IsDir() {
		return fmt.Errorf("no routing log database at %s", path)
	}
	return nil
}

// writeLogsReport prints the text report in a fixed field order, so two runs over
// the same rows produce byte-identical output. Empty optional fields are omitted,
// and recommendation confidence uses the same semantics as the log API.
func writeLogsReport(output io.Writer, events []storage.StoredEvent) {
	fmt.Fprintf(output, "routing log check: %d entries, newest first\n", len(events))
	fmt.Fprintf(output, "no upstream call, no agent, no retention sweep; opening an older database may apply schema migrations\n")
	fmt.Fprintf(output, "status is the client HTTP result; jev_status is group recommendation; usage status is upstream token extraction\n")
	fmt.Fprintf(output, "usage oversized is not context overflow; none means unknown, not zero; bytes_written is response bytes\n")
	fmt.Fprintf(output, "optional fields are omitted when unknown/inapplicable; diagnostics are audit metadata, not routing inputs\n\n")
	for _, entry := range events {
		event := entry.Event
		fmt.Fprintf(output, "id=%d request_id=%s\n", entry.ID, event.RequestID)
		fmt.Fprintf(output, "  started_at=%s duration_ms=%d bytes_written=%d\n",
			event.StartedAt.UTC().Format(time.RFC3339Nano), event.DurationMS, event.BytesWritten)
		fmt.Fprintf(output, "  protocol=%s stream=%t status=%d", event.Protocol, event.Stream, event.Status)
		if event.UpstreamStatus != 0 {
			fmt.Fprintf(output, " upstream_status=%d", event.UpstreamStatus)
		}
		if event.ErrorCode != "" {
			fmt.Fprintf(output, " error_code=%s", event.ErrorCode)
		}
		fmt.Fprintln(output)
		for _, field := range []struct{ name, value string }{
			{"routing_mode", event.RoutingMode}, {"selection_mode", event.SelectionMode},
			{"requested_model", event.RequestedModel}, {"effective_model", event.EffectiveModel},
			{"provider", event.ProviderKey}, {"upstream_model", event.UpstreamModel},
		} {
			if field.value != "" {
				fmt.Fprintf(output, "  %s=%s\n", field.name, field.value)
			}
		}
		fmt.Fprintf(output, "  attempts=%d failover=%t\n", event.GatewayAttempts, event.FailoverUsed)
		if event.ClientIP != "" {
			fmt.Fprintf(output, "  client_ip=%s\n", event.ClientIP)
		}
		if event.JevStatus != "" {
			fmt.Fprintf(output, "  jev_status=%s", event.JevStatus)
			if confidence := event.RecommendationConfidence(entry.JevTrace); confidence != nil {
				fmt.Fprintf(output, " confidence=%s", formatStoredOptionalFloat(confidence))
			}
			if event.FallbackReason != "" {
				fmt.Fprintf(output, " fallback=%s", event.FallbackReason)
			}
			fmt.Fprintln(output)
		}
		if event.RoutingLatencyMS != nil {
			fmt.Fprintf(output, "  routing_latency_ms=%s\n", formatStoredOptionalInt(event.RoutingLatencyMS))
		}
		if event.JevLatencyMS != nil {
			fmt.Fprintf(output, "  jev_latency_ms=%s\n", formatStoredOptionalInt(event.JevLatencyMS))
		}
		if diagnostics := event.DiagnosticDetails(entry.JevTrace); diagnostics != nil {
			data, _ := json.Marshal(diagnostics)
			fmt.Fprintf(output, "  diagnostics=%s\n", data)
		}
		fmt.Fprintf(output, "  usage status=%s", event.Usage.Status)
		if event.Usage.Source != "" {
			fmt.Fprintf(output, " source=%s", event.Usage.Source)
		}
		fmt.Fprintf(output, " input_tokens=%s output_tokens=%s total_tokens=%s\n", formatStoredOptionalInt(event.Usage.InputTokens),
			formatStoredOptionalInt(event.Usage.OutputTokens), formatStoredOptionalInt(event.Usage.TotalTokens))
		if entry.JevTrace != nil {
			writeStoredTrace(output, entry.JevTrace)
		}
		fmt.Fprintf(output, "\n")
	}
}

// writeStoredTrace prints the optional Jev trace. It is diagnostic data, so it is
// printed under the event it belongs to rather than as a row of its own.
func writeStoredTrace(output io.Writer, trace *logging.JevTrace) {
	fmt.Fprintf(output, "  jev input_mode=%s candidate_count=%d candidate_groups=%s", trace.InputMode, trace.CandidateCount, formatStringsOrNone(trace.CandidateGroups))
	if trace.RecommendedGroup != "" {
		fmt.Fprintf(output, " recommended_group=%s", trace.RecommendedGroup)
	}
	if trace.SelectedGroup != "" {
		fmt.Fprintf(output, " selected_group=%s", trace.SelectedGroup)
	}
	if trace.FailureReason != "" {
		fmt.Fprintf(output, " failure_reason=%s", trace.FailureReason)
	}
	fmt.Fprintln(output)
	// Keep historical model-level trace data readable without suggesting these
	// empty fields participate in the current group-level recommendation.
	if trace.ModelCount > 0 || len(trace.CandidateModels) > 0 || trace.Selected != "" || len(trace.Probabilities) > 0 {
		fmt.Fprintf(output, "  jev legacy_model_count=%d legacy_selected_model=%s legacy_candidate_models=%s\n",
			trace.ModelCount, orNone(trace.Selected), formatStringsOrNone(trace.CandidateModels))
		distribution := make([]string, 0, len(trace.Probabilities))
		for _, probability := range trace.Probabilities {
			distribution = append(distribution, fmt.Sprintf("%s=%.6f", probability.Model, probability.Probability))
		}
		fmt.Fprintf(output, "  jev legacy_model_distribution=%s\n", formatStringsOrNone(distribution))
	}
	groupDistribution := make([]string, 0, len(trace.GroupProbabilities))
	for _, probability := range trace.GroupProbabilities {
		groupDistribution = append(groupDistribution, fmt.Sprintf("%s=%.6f", probability.Group, probability.Probability))
	}
	if len(groupDistribution) > 0 {
		fmt.Fprintf(output, "  jev group_distribution=%s\n", formatStringsOrNone(groupDistribution))
	}
}

// logsCheckJSONEntry is the JSON shape of one report line. The keys match the text
// labels so an operator can move between the two without a second vocabulary, and
// the nested structs keep an absent field out of the output entirely.
type logsCheckJSONEntry struct {
	ID        int64              `json:"id"`
	Event     logsCheckJSONEvent `json:"event"`
	JevTrace  *logsCheckJSONJev  `json:"jev_trace,omitempty"`
	JevCallID *int64             `json:"jev_call_id,omitempty"`
}

type logsCheckJSONEvent struct {
	RequestID        string               `json:"request_id"`
	StartedAt        string               `json:"started_at"`
	DurationMS       int64                `json:"duration_ms"`
	Protocol         string               `json:"protocol"`
	RoutingMode      string               `json:"routing_mode,omitempty"`
	SelectionMode    string               `json:"selection_mode,omitempty"`
	RequestedModel   string               `json:"requested_model,omitempty"`
	EffectiveModel   string               `json:"effective_model,omitempty"`
	ProviderKey      string               `json:"provider_key,omitempty"`
	UpstreamModel    string               `json:"upstream_model,omitempty"`
	Status           int                  `json:"status"`
	UpstreamStatus   *int                 `json:"upstream_status,omitempty"`
	ErrorCode        string               `json:"error_code,omitempty"`
	Stream           bool                 `json:"stream"`
	BytesWritten     int64                `json:"bytes_written"`
	ClientIP         string               `json:"client_ip,omitempty"`
	JevStatus        string               `json:"jev_status,omitempty"`
	Confidence       *float64             `json:"confidence,omitempty"`
	FallbackReason   string               `json:"fallback_reason,omitempty"`
	Diagnostics      *logging.Diagnostics `json:"diagnostics,omitempty"`
	GatewayAttempts  int                  `json:"gateway_attempts"`
	FailoverUsed     bool                 `json:"failover_used"`
	RoutingLatencyMS *int64               `json:"routing_latency_ms,omitempty"`
	JevLatencyMS     *int64               `json:"jev_latency_ms,omitempty"`
	Usage            struct {
		Status       string `json:"status"`
		Source       string `json:"source,omitempty"`
		InputTokens  *int64 `json:"input_tokens,omitempty"`
		OutputTokens *int64 `json:"output_tokens,omitempty"`
		TotalTokens  *int64 `json:"total_tokens,omitempty"`
	} `json:"usage"`
}

type logsCheckJSONJev struct {
	FailureReason      string                      `json:"failure_reason,omitempty"`
	InputMode          string                      `json:"input_mode"`
	Selected           string                      `json:"selected_model,omitempty"`
	CandidateModels    []string                    `json:"candidate_models,omitempty"`
	ModelCount         int                         `json:"model_count,omitempty"`
	CandidateCount     int                         `json:"candidate_count"`
	CandidateGroups    []string                    `json:"candidate_groups"`
	RecommendedGroup   string                      `json:"recommended_group,omitempty"`
	SelectedGroup      string                      `json:"selected_group,omitempty"`
	Probabilities      []logsCheckModelProbability `json:"probabilities,omitempty"`
	GroupProbabilities []logging.GroupProbability  `json:"group_probabilities,omitempty"`
}

type logsCheckModelProbability struct {
	Model       string  `json:"model"`
	Probability float64 `json:"probability"`
}

// writeLogsJSON prints one JSON object per entry. The values are the stored ones:
// an absent token count is omitted rather than written as zero.
func writeLogsJSON(output io.Writer, events []storage.StoredEvent) error {
	encoder := json.NewEncoder(output)
	encoder.SetEscapeHTML(false)
	for _, entry := range events {
		event := entry.Event
		line := logsCheckJSONEntry{
			ID: entry.ID,
			Event: logsCheckJSONEvent{
				RequestID:        event.RequestID,
				StartedAt:        event.StartedAt.UTC().Format(time.RFC3339Nano),
				DurationMS:       event.DurationMS,
				Protocol:         event.Protocol,
				RoutingMode:      event.RoutingMode,
				SelectionMode:    event.SelectionMode,
				RequestedModel:   event.RequestedModel,
				EffectiveModel:   event.EffectiveModel,
				ProviderKey:      event.ProviderKey,
				UpstreamModel:    event.UpstreamModel,
				Status:           event.Status,
				UpstreamStatus:   optionalIntPointer(event.UpstreamStatus),
				ErrorCode:        event.ErrorCode,
				Stream:           event.Stream,
				BytesWritten:     event.BytesWritten,
				ClientIP:         event.ClientIP,
				JevStatus:        event.JevStatus,
				Confidence:       event.RecommendationConfidence(entry.JevTrace),
				FallbackReason:   event.FallbackReason,
				Diagnostics:      event.DiagnosticDetails(entry.JevTrace),
				GatewayAttempts:  event.GatewayAttempts,
				FailoverUsed:     event.FailoverUsed,
				RoutingLatencyMS: event.RoutingLatencyMS,
				JevLatencyMS:     event.JevLatencyMS,
			},
			JevCallID: entry.JevCallID,
		}
		line.Event.Usage.Status = event.Usage.Status
		line.Event.Usage.Source = event.Usage.Source
		line.Event.Usage.InputTokens = event.Usage.InputTokens
		line.Event.Usage.OutputTokens = event.Usage.OutputTokens
		line.Event.Usage.TotalTokens = event.Usage.TotalTokens
		if entry.JevTrace != nil {
			trace := entry.JevTrace
			rendered := &logsCheckJSONJev{
				FailureReason:      trace.FailureReason,
				InputMode:          trace.InputMode,
				Selected:           trace.Selected,
				CandidateModels:    trace.CandidateModels,
				ModelCount:         trace.ModelCount,
				CandidateCount:     trace.CandidateCount,
				CandidateGroups:    append([]string{}, trace.CandidateGroups...),
				RecommendedGroup:   trace.RecommendedGroup,
				SelectedGroup:      trace.SelectedGroup,
				GroupProbabilities: trace.GroupProbabilities,
			}
			for _, probability := range trace.Probabilities {
				rendered.Probabilities = append(rendered.Probabilities, logsCheckModelProbability{Model: probability.Model, Probability: probability.Probability})
			}
			line.JevTrace = rendered
		}
		if err := encoder.Encode(line); err != nil {
			return fmt.Errorf("write routing log entry: %w", err)
		}
	}
	return nil
}

// optionalIntPointer maps a zero status onto nil for the JSON report.
func optionalIntPointer(value int) *int {
	if value == 0 {
		return nil
	}
	copied := value
	return &copied
}

func formatStoredOptionalInt(value *int64) string {
	if value == nil {
		return "none"
	}
	return fmt.Sprintf("%d", *value)
}

func formatStoredOptionalFloat(value *float64) string {
	if value == nil {
		return "none"
	}
	return fmt.Sprintf("%.6f", *value)
}

func formatStringsOrNone(values []string) string {
	if len(values) == 0 {
		return "none"
	}
	return strings.Join(values, ", ")
}

func orNone(value string) string {
	if value == "" {
		return "none"
	}
	return value
}
