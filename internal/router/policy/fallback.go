package policy

import (
	"errors"

	"github.com/nextroad-dev/Auto-Router/internal/router/decision"
)

// JevSentinels is the caller's classification table: the five sentinel errors the
// Jev client returns. Stage 7 owns the call and therefore owns the client's
// sentinel values, and it passes them here rather than translating them itself.
//
// The direction matters. If the orchestration mapped errors onto fallback reasons,
// two call sites could disagree and a new sentinel could be forgotten silently.
// With the table supplied here, this package owns the classification and the
// dependency guard keeps the client out of it: the policy engine still imports
// nothing from the Jev client.
type JevSentinels struct {
	// Timeout is the sentinel for a call that exceeded its deadline.
	Timeout error
	// Unavailable is the sentinel for an endpoint that could not be reached or
	// reported a temporary failure.
	Unavailable error
	// Rejected is the sentinel for a request the endpoint refused.
	Rejected error
	// Canceled is the sentinel for a caller-canceled call.
	Canceled error
	// InvalidResult is the sentinel for a response that parsed but failed
	// validation.
	InvalidResult error
}

// WrapFallback classifies one Jev call error. A nil error yields ReasonNone, which
// is the "the call succeeded" case. An error that matches none of the known
// sentinels yields false, and the caller decides what an unrecognized failure
// means: this function never invents a reason it cannot justify.
//
// Matching uses errors.Is, so a sentinel wrapped by the client (for example
// "jev request timed out: context deadline exceeded") is still classified.
func WrapFallback(err error, sentinels JevSentinels) (decision.FallbackReason, bool) {
	if err == nil {
		return decision.ReasonNone, true
	}
	for _, mapping := range []struct {
		sentinel error
		reason   decision.FallbackReason
	}{
		{sentinels.Timeout, decision.ReasonJevTimeout},
		{sentinels.Unavailable, decision.ReasonJevUnavailable},
		{sentinels.Rejected, decision.ReasonJevRejected},
		{sentinels.Canceled, decision.ReasonJevCanceled},
		{sentinels.InvalidResult, decision.ReasonJevInvalidResult},
	} {
		if mapping.sentinel != nil && errors.Is(err, mapping.sentinel) {
			return mapping.reason, true
		}
	}
	return decision.ReasonNone, false
}

// JevSignalFromResult builds the engine input from a successful call. It exists so
// the three fields that must agree — availability, the selected model and the
// confidence — are set in one place rather than at each call site.
func JevSignalFromResult(selected string, confidence float64, probabilities map[string]float64) JevSignal {
	return JevSignal{
		Available:     true,
		Selected:      selected,
		Confidence:    confidence,
		Probabilities: probabilities,
	}
}

// JevSignalFromError builds the engine input from a failed or skipped call. An
// unrecognized error is reported as not_requested rather than as a fabricated
// failure: an operator reading the routing log sees "no recommendation", which is
// true, instead of a specific cause that may be wrong.
func JevSignalFromError(err error, sentinels JevSentinels) JevSignal {
	if err == nil {
		return NotRequested()
	}
	if reason, ok := WrapFallback(err, sentinels); ok && reason != decision.ReasonNone {
		return JevSignal{Available: false, Failure: reason}
	}
	return NotRequested()
}

// NotRequested is the signal for a request where Jev was never called. It is
// distinct from a failure: a caller that skipped the call must say so rather than
// let the engine guess.
func NotRequested() JevSignal {
	return JevSignal{Available: false, Failure: decision.ReasonNotRequested}
}
