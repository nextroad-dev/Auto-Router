package config

// RoutingAutoConfig is the `routing.auto` section: the settings of the automatic
// routing path (`model=auto`). It is deliberately small. Everything that must be
// reproducible across deployments — the retry bound, the retryable error class,
// the Jev input-mode semantics and the bounded redaction constants — is a named
// constant in code, not a setting.
type RoutingAutoConfig struct {
	// Failover allows one additional upstream attempt after a transport failure
	// that may not have reached the provider. It only ever controls whether the
	// extra attempt is made: the attempt bound and the retryable class are fixed
	// (see internal/router/auto).
	Failover RoutingAutoFailoverConfig `json:"failover"`
}

// RoutingAutoFailoverConfig is the failover switch.
type RoutingAutoFailoverConfig struct {
	Enabled bool `json:"enabled"`
}

// defaultRoutingAutoConfig ships failover on: the only failure it can retry is
// one that the upstream never saw, and the attempt count is bounded at two in
// code. A deployment that would rather fail fast turns it off.
func defaultRoutingAutoConfig() RoutingAutoConfig {
	return RoutingAutoConfig{
		Failover: RoutingAutoFailoverConfig{Enabled: true},
	}
}

// Validate checks the automatic routing section before any listener is bound.
// Both fields are booleans, so there is no value to reject; the method exists so
// the section is validated through the same path as every other section and a
// future field cannot be added without a place to check it.
func (a RoutingAutoConfig) Validate() error { return nil }
