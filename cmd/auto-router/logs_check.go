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
// the same rows produce byte-identical output. Every line is a label and a value
// that came out of a column; nothing is derived and nothing is summarized.
func writeLogsReport(output io.Writer, events []storage.StoredEvent) {
	fmt.Fprintf(output, "routing log check: %d entries, newest first\n", len(events))
	fmt.Fprintf(output, "no upstream call, no agent, no retention sweep, the database was not modified\n\n")
	for _, entry := range events {
		event := entry.Event
		fmt.Fprintf(output, "id=%d request_id=%s\n", entry.ID, event.RequestID)
		fmt.Fprintf(output, "  started_at=%s duration_ms=%d bytes_written=%d\n",
			event.StartedAt.UTC().Format(time.RFC3339Nano), event.DurationMS, event.BytesWritten)
		fmt.Fprintf(output, "  protocol=%s stream=%t status=%d upstream_status=%s error_code=%s\n",
			event.Protocol, event.Stream, event.Status, formatOptionalStatus(event.UpstreamStatus), orNone(event.ErrorCode))
		fmt.Fprintf(output, "  routing_mode=%s selection_mode=%s requested_model=%s\n",
			orNone(event.RoutingMode), orNone(event.SelectionMode), orNone(event.RequestedModel))
		fmt.Fprintf(output, "  effective_model=%s jev_top_model=%s\n",
			orNone(event.EffectiveModel), orNone(event.JevTopModel))
		fmt.Fprintf(output, "  provider=%s upstream_model=%s attempts=%d failover=%t\n",
			orNone(event.ProviderKey), orNone(event.UpstreamModel), event.GatewayAttempts, event.FailoverUsed)
		fmt.Fprintf(output, "  preference=%s source=%s client_ip=%s\n",
			orNone(event.RoutingPreference), orNone(event.PreferenceSource), orNone(event.ClientIP))
		fmt.Fprintf(output, "  jev_status=%s confidence=%s band=%s fallback=%s evidence_hash=%s\n",
			orNone(event.JevStatus), formatStoredOptionalFloat(event.Confidence), orNone(event.ConfidenceBand),
			orNone(event.FallbackReason), orNone(event.EvidenceHash))
		fmt.Fprintf(output, "  routing_latency_ms=%s jev_latency_ms=%s\n",
			formatStoredOptionalInt(event.RoutingLatencyMS), formatStoredOptionalInt(event.JevLatencyMS))
		fmt.Fprintf(output, "  usage status=%s source=%s input_tokens=%s output_tokens=%s total_tokens=%s\n",
			event.Usage.Status, orNone(event.Usage.Source), formatStoredOptionalInt(event.Usage.InputTokens),
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
	fmt.Fprintf(output, "  jev status=%s failure=%s input_mode=%s latency_ms=%s\n",
		trace.Status, orNone(trace.FailureReason), trace.InputMode, formatStoredOptionalInt(trace.LatencyMS))
	fmt.Fprintf(output, "  jev model_count=%d candidate_count=%d selected=%s\n",
		trace.ModelCount, trace.CandidateCount, orNone(trace.Selected))
	fmt.Fprintf(output, "  jev confidence=%s band=%s fallback=%s evidence_hash=%s\n",
		formatStoredOptionalFloat(trace.Confidence), orNone(trace.ConfidenceBand),
		orNone(trace.FallbackReason), orNone(trace.EvidenceHash))
	fmt.Fprintf(output, "  jev candidates=%s\n", formatStringsOrNone(trace.CandidateModels))
	distribution := make([]string, 0, len(trace.Probabilities))
	for _, probability := range trace.Probabilities {
		distribution = append(distribution, fmt.Sprintf("%s=%.6f", probability.Model, probability.Probability))
	}
	fmt.Fprintf(output, "  jev distribution=%s\n", formatStringsOrNone(distribution))
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
	RequestID         string   `json:"request_id"`
	StartedAt         string   `json:"started_at"`
	DurationMS        int64    `json:"duration_ms"`
	Protocol          string   `json:"protocol"`
	RoutingMode       string   `json:"routing_mode,omitempty"`
	SelectionMode     string   `json:"selection_mode,omitempty"`
	RequestedModel    string   `json:"requested_model,omitempty"`
	EffectiveModel    string   `json:"effective_model,omitempty"`
	JevTopModel       string   `json:"jev_top_model,omitempty"`
	ProviderKey       string   `json:"provider_key,omitempty"`
	UpstreamModel     string   `json:"upstream_model,omitempty"`
	Status            int      `json:"status"`
	UpstreamStatus    *int     `json:"upstream_status,omitempty"`
	ErrorCode         string   `json:"error_code,omitempty"`
	Stream            bool     `json:"stream"`
	BytesWritten      int64    `json:"bytes_written"`
	ClientIP          string   `json:"client_ip,omitempty"`
	RoutingPreference string   `json:"routing_preference,omitempty"`
	PreferenceSource  string   `json:"preference_source,omitempty"`
	JevStatus         string   `json:"jev_status,omitempty"`
	Confidence        *float64 `json:"confidence,omitempty"`
	ConfidenceBand    string   `json:"confidence_band,omitempty"`
	FallbackReason    string   `json:"fallback_reason,omitempty"`
	EvidenceHash      string   `json:"evidence_hash,omitempty"`
	GatewayAttempts   int      `json:"gateway_attempts"`
	FailoverUsed      bool     `json:"failover_used"`
	RoutingLatencyMS  *int64   `json:"routing_latency_ms,omitempty"`
	JevLatencyMS      *int64   `json:"jev_latency_ms,omitempty"`
	Usage             struct {
		Status       string `json:"status"`
		Source       string `json:"source,omitempty"`
		InputTokens  *int64 `json:"input_tokens,omitempty"`
		OutputTokens *int64 `json:"output_tokens,omitempty"`
		TotalTokens  *int64 `json:"total_tokens,omitempty"`
	} `json:"usage"`
}

type logsCheckJSONJev struct {
	Status          string    `json:"status"`
	FailureReason   string    `json:"failure_reason,omitempty"`
	InputMode       string    `json:"input_mode"`
	Selected        string    `json:"selected,omitempty"`
	ConfidenceBand  string    `json:"confidence_band,omitempty"`
	FallbackReason  string    `json:"fallback_reason,omitempty"`
	EvidenceHash    string    `json:"evidence_hash,omitempty"`
	LatencyMS       *int64    `json:"latency_ms,omitempty"`
	CandidateModels []string  `json:"candidate_models"`
	ModelCount      int       `json:"model_count"`
	CandidateCount  int       `json:"candidate_count"`
	Confidence      *float64  `json:"confidence,omitempty"`
	Probabilities   []float64 `json:"probabilities,omitempty"`
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
				RequestID:         event.RequestID,
				StartedAt:         event.StartedAt.UTC().Format(time.RFC3339Nano),
				DurationMS:        event.DurationMS,
				Protocol:          event.Protocol,
				RoutingMode:       event.RoutingMode,
				SelectionMode:     event.SelectionMode,
				RequestedModel:    event.RequestedModel,
				EffectiveModel:    event.EffectiveModel,
				JevTopModel:       event.JevTopModel,
				ProviderKey:       event.ProviderKey,
				UpstreamModel:     event.UpstreamModel,
				Status:            event.Status,
				UpstreamStatus:    optionalIntPointer(event.UpstreamStatus),
				ErrorCode:         event.ErrorCode,
				Stream:            event.Stream,
				BytesWritten:      event.BytesWritten,
				ClientIP:          event.ClientIP,
				RoutingPreference: event.RoutingPreference,
				PreferenceSource:  event.PreferenceSource,
				JevStatus:         event.JevStatus,
				Confidence:        event.Confidence,
				ConfidenceBand:    event.ConfidenceBand,
				FallbackReason:    event.FallbackReason,
				EvidenceHash:      event.EvidenceHash,
				GatewayAttempts:   event.GatewayAttempts,
				FailoverUsed:      event.FailoverUsed,
				RoutingLatencyMS:  event.RoutingLatencyMS,
				JevLatencyMS:      event.JevLatencyMS,
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
				Status:          trace.Status,
				FailureReason:   trace.FailureReason,
				InputMode:       trace.InputMode,
				Selected:        trace.Selected,
				ConfidenceBand:  trace.ConfidenceBand,
				FallbackReason:  trace.FallbackReason,
				EvidenceHash:    trace.EvidenceHash,
				LatencyMS:       trace.LatencyMS,
				CandidateModels: trace.CandidateModels,
				ModelCount:      trace.ModelCount,
				CandidateCount:  trace.CandidateCount,
				Confidence:      trace.Confidence,
			}
			if rendered.CandidateModels == nil {
				rendered.CandidateModels = []string{}
			}
			for _, probability := range trace.Probabilities {
				rendered.Probabilities = append(rendered.Probabilities, probability.Probability)
			}
			line.JevTrace = rendered
		}
		if err := encoder.Encode(line); err != nil {
			return fmt.Errorf("write routing log entry: %w", err)
		}
	}
	return nil
}

// formatOptionalStatus renders an upstream status, where zero means "no upstream
// response was read" and is printed as absent rather than as a status.
func formatOptionalStatus(value int) string {
	if value == 0 {
		return "none"
	}
	return fmt.Sprintf("%d", value)
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
