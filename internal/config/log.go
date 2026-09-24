package config

import (
	"fmt"
	"strconv"
	"strings"
)

// The routing log's bounds. They are configuration because retention is an
// operational decision, and constants where a wrong value could break the
// request path.
const (
	// maxRetentionDays caps routing.log.retention_days and
	// routing.log.jev_trace.retention_days. Ten years is longer than any
	// deployment of this router will last; the cap exists so a decimal typo
	// ("300000") is refused instead of silently disabling deletion.
	maxRetentionDays = 3650
	// RoutingLogRetentionDaysDefault keeps one month of routing events. It is long
	// enough to answer "what did the router do last week" and short enough that an
	// unmanaged database does not grow without limit.
	RoutingLogRetentionDaysDefault = 30
	// RoutingLogJevRetentionDaysDefault keeps one week of Jev traces. The trace is
	// a diagnostic: the event keeps the decision, the trace keeps the distribution,
	// and the diagnostic is the first thing that should expire.
	RoutingLogJevRetentionDaysDefault = 7
)

// RoutingLogConfig is the `routing.log` section: the durable routing log.
//
// It is deliberately separate from the top-level `log` section, which configures
// the process log. Both may be present at once: `log.level` decides what the
// process writes to stderr, `routing.log` decides what survives a restart. The
// stored record is made of identifiers, counts, durations and closed
// enumerations; there is no setting that would make it store a prompt, a tool
// schema or a credential.
type RoutingLogConfig struct {
	// Enabled turns durable routing logging on. It is on by default: "what did
	// the router do" is the product's core question, and a log that has to be
	// enabled before an incident is a log that is missing during one. Turning it
	// off removes the observer from the forwarding path entirely, so a disabled
	// process is byte-for-byte the stage-7 process.
	Enabled bool `json:"enabled"`
	// StoreClientIP writes the observed client address into the log. It is off by
	// default: the address is the one field in the record that is personal data,
	// it is not needed to explain a routing decision, and the process log already
	// carries it. Enabling it prints a startup WARN.
	StoreClientIP bool `json:"store_client_ip"`
	// RetentionDays bounds how long a routing event is kept. Zero means "keep
	// forever", which is legal but warns: an unbounded log grows until the disk
	// says otherwise.
	RetentionDays int `json:"retention_days"`
	// JevTrace is the optional Jev diagnostic trace.
	JevTrace RoutingLogJevTraceConfig `json:"jev_trace"`
}

// RoutingLogJevTraceConfig is the optional debug trace of the Jev calls the
// automatic path made. It stores metadata only — statuses, identifiers, counts,
// latencies and the normalized distribution — and is off by default because it is
// a debugging aid rather than part of the audit record.
type RoutingLogJevTraceConfig struct {
	Enabled bool `json:"enabled"`
	// RetentionDays bounds how long a trace is kept. Zero means "keep forever".
	RetentionDays int `json:"retention_days"`
}

func defaultRoutingLogConfig() RoutingLogConfig {
	return RoutingLogConfig{
		Enabled:       true,
		StoreClientIP: false,
		RetentionDays: RoutingLogRetentionDaysDefault,
		JevTrace: RoutingLogJevTraceConfig{
			Enabled:       false,
			RetentionDays: RoutingLogJevRetentionDaysDefault,
		},
	}
}

// Validate checks the section before any listener is bound. Both retention values
// are checked by the same rule, and the error never repeats the offending number:
// it names the setting and the accepted range so an operator can fix the file
// without a value being echoed back into a log.
func (l RoutingLogConfig) Validate() error {
	for _, retention := range []struct {
		name  string
		value int
	}{
		{"routing.log.retention_days", l.RetentionDays},
		{"routing.log.jev_trace.retention_days", l.JevTrace.RetentionDays},
	} {
		if retention.value < 0 || retention.value > maxRetentionDays {
			return fmt.Errorf("%s must be a whole number of days between 0 and %d, where 0 keeps rows forever", retention.name, maxRetentionDays)
		}
	}
	return nil
}

// routingLogWarnings reports routing log states that are valid but likely to
// surprise the operator. It never includes a client address, which is the one
// value the section can make storable.
func (c Config) routingLogWarnings() []string {
	var warnings []string
	if !c.Routing.Log.Enabled {
		// Silencing the durable log is a decision with an operational cost, so it
		// is stated at startup rather than discovered during an incident.
		warnings = append(warnings, "routing.log.enabled is false; routing decisions will not be recorded and cannot be reviewed after a restart")
		return warnings
	}
	if c.Routing.Log.StoreClientIP {
		warnings = append(warnings, "routing.log.store_client_ip is enabled; the observed client address will be written into the routing log, which is personal data")
	}
	if c.Routing.Log.RetentionDays == 0 {
		warnings = append(warnings, "routing.log.retention_days is 0; routing events are kept forever and the database will grow without a bound")
	}
	if c.Routing.Log.JevTrace.Enabled && c.Routing.Log.JevTrace.RetentionDays == 0 {
		warnings = append(warnings, "routing.log.jev_trace.retention_days is 0; Jev traces are kept forever and the database will grow without a bound")
	}
	return warnings
}

// errRetentionDays is the shared error for a retention value outside the range.
func errRetentionDays(name string) error {
	return fmt.Errorf("%s must be a whole number of days between 0 and %d", name, maxRetentionDays)
}

// parseRetentionDays parses one environment value. An explicitly empty value is
// rejected: "no retention configured" is a state this router does not have, and
// silently falling back to the file would hide the mistake. The error never
// repeats the offending value.
func parseRetentionDays(name, value string) (int, error) {
	if value == "" || value != strings.TrimSpace(value) {
		return 0, errRetentionDays(name)
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed < 0 || parsed > maxRetentionDays {
		return 0, errRetentionDays(name)
	}
	return parsed, nil
}
