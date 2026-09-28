package config

import (
	"fmt"
)

const (
	minAutoUpstreamAttempts = 1
	maxAutoUpstreamAttempts = 8
)

// RoutingAutoConfig is the `routing.auto` section: the settings of the automatic
// routing path (`model=auto`).
type RoutingAutoConfig struct {
	// DefaultGroup is used when Jev is unavailable or its confidence falls below
	// the live routing threshold. It must be one of the three fixed routing groups.
	DefaultGroup string `json:"default_group"`
	// Failover controls bounded retries within the selected routing group.
	Failover RoutingAutoFailoverConfig `json:"failover"`
}

// RoutingAutoFailoverConfig is the runtime failover policy. MaxAttempts includes
// the first upstream request. RetryOn.StatusCodes is intentionally empty by default:
// retrying an HTTP response may duplicate work or charges at the provider.
type RoutingAutoFailoverConfig struct {
	Enabled     bool                   `json:"enabled"`
	MaxAttempts int                    `json:"max_attempts"`
	RetryOn     RoutingAutoRetryConfig `json:"retry_on"`
}

// RoutingAutoRetryConfig lists the upstream failure classes that may advance to
// the next configured pair in the selected group.
type RoutingAutoRetryConfig struct {
	PreRequestFailure bool  `json:"pre_request_failure"`
	Timeout           bool  `json:"timeout"`
	StatusCodes       []int `json:"status_codes"`
}

func defaultRoutingAutoConfig() RoutingAutoConfig {
	return RoutingAutoConfig{
		DefaultGroup: "medium",
		Failover: RoutingAutoFailoverConfig{
			Enabled:     true,
			MaxAttempts: 2,
			RetryOn: RoutingAutoRetryConfig{
				PreRequestFailure: true,
				StatusCodes:       []int{},
			},
		},
	}
}

// Validate checks the automatic routing section before any listener is bound.
func (a RoutingAutoConfig) Validate() error {
	switch a.DefaultGroup {
	case "simple", "medium", "complex":
	default:
		return fmt.Errorf("routing.auto.default_group must be simple, medium or complex")
	}
	if a.Failover.MaxAttempts < minAutoUpstreamAttempts || a.Failover.MaxAttempts > maxAutoUpstreamAttempts {
		return fmt.Errorf("routing.auto.failover.max_attempts must be between %d and %d", minAutoUpstreamAttempts, maxAutoUpstreamAttempts)
	}
	seen := make(map[int]struct{}, len(a.Failover.RetryOn.StatusCodes))
	for _, status := range a.Failover.RetryOn.StatusCodes {
		if !retryableUpstreamStatus(status) {
			return fmt.Errorf("routing.auto.failover.retry_on.status_codes contains an unsupported status")
		}
		if _, duplicate := seen[status]; duplicate {
			return fmt.Errorf("routing.auto.failover.retry_on.status_codes must not contain duplicates")
		}
		seen[status] = struct{}{}
	}
	return nil
}

// retryableUpstreamStatus is a closed allowlist of transient-looking upstream
// responses. Operators must still opt into each status because none proves that
// the provider did no work or charged nothing.
func retryableUpstreamStatus(status int) bool {
	switch status {
	case 408, 425, 429, 500, 502, 503, 504:
		return true
	default:
		return false
	}
}
