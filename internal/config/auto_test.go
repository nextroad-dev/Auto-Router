package config

import "testing"

func TestDefaultAutoFailoverIsBoundedAndConservative(t *testing.T) {
	defaults := Defaults().Routing.Auto
	if defaults.DefaultGroup != "medium" {
		t.Fatalf("default group = %q, want medium", defaults.DefaultGroup)
	}
	if defaults.Failover.MaxAttempts != 2 {
		t.Fatalf("default maximum attempts = %d, want 2 total attempts", defaults.Failover.MaxAttempts)
	}
	if !defaults.Failover.RetryOn.PreRequestFailure || defaults.Failover.RetryOn.Timeout || len(defaults.Failover.RetryOn.StatusCodes) != 0 {
		t.Fatalf("default retry policy is not conservative: %+v", defaults.Failover.RetryOn)
	}
	if err := defaults.Validate(); err != nil {
		t.Fatalf("defaults failed validation: %v", err)
	}
}

func TestAutoConfigRejectsInvalidGroupsAttemptsAndRetryStatuses(t *testing.T) {
	valid := defaultRoutingAutoConfig()
	cases := []struct {
		name string
		edit func(*RoutingAutoConfig)
	}{
		{name: "unknown group", edit: func(cfg *RoutingAutoConfig) { cfg.DefaultGroup = "other" }},
		{name: "attempt count below one", edit: func(cfg *RoutingAutoConfig) { cfg.Failover.MaxAttempts = 0 }},
		{name: "attempt count above group capacity", edit: func(cfg *RoutingAutoConfig) { cfg.Failover.MaxAttempts = 9 }},
		{name: "non-transient status", edit: func(cfg *RoutingAutoConfig) { cfg.Failover.RetryOn.StatusCodes = []int{401} }},
		{name: "duplicate status", edit: func(cfg *RoutingAutoConfig) { cfg.Failover.RetryOn.StatusCodes = []int{429, 429} }},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			candidate := valid
			test.edit(&candidate)
			if err := candidate.Validate(); err == nil {
				t.Fatal("invalid auto configuration passed validation")
			}
		})
	}
}
