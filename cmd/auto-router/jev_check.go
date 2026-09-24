package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sort"
	"time"

	"github.com/nextroad-dev/Auto-Router/internal/config"
	"github.com/nextroad-dev/Auto-Router/internal/models"
	"github.com/nextroad-dev/Auto-Router/internal/providers"
	"github.com/nextroad-dev/Auto-Router/internal/router/jev"
)

// checkPrompt is the built-in sentence sent to TypeSafe when -jev-check runs
// without an override. It is deliberately about a decision Jev can make and
// contains no user data; the output states that it leaves the machine.
const checkPrompt = "A user asks a short factual question with no images, no tools and no code. Pick the smallest capable model."

// maxCheckCandidates bounds how many pairs the maintenance check sends. It
// matches the client's own candidate limit (the verified Choice option limit)
// so the check can never build a request the client would refuse.
const maxCheckCandidates = 255

// newJevClient builds the System One client. An unconfigured section yields a
// nil client and no error: the process must start and behave exactly as it did
// before stage 4 when Jev is disabled.
func newJevClient(cfg config.Config, logger *slog.Logger) (*jev.Client, error) {
	if !cfg.Jev.Enabled {
		return nil, nil
	}
	client, err := jev.New(jev.Config{
		BaseURL:      cfg.Jev.BaseURL,
		APIKey:       cfg.Jev.APIKey,
		Model:        cfg.Jev.Model,
		AuthHeader:   cfg.Jev.AuthHeader,
		AuthScheme:   cfg.Jev.AuthScheme,
		Timeout:      time.Duration(cfg.Jev.Timeout),
		CaptureRawIO: cfg.Jev.CaptureRawIO,
	})
	if err != nil {
		return nil, fmt.Errorf("configure jev: %w", err)
	}
	// The base URL and the pinned model are safe to log. The credential never is.
	logger.Info("jev client configured",
		"base_url", cfg.Jev.BaseURL,
		"model", cfg.Jev.Model,
		"auth_header", cfg.Jev.AuthHeader,
		"api_key", models.MaskSecret(cfg.Jev.APIKey),
		"capture_raw_io", cfg.Jev.CaptureRawIO,
	)
	return client, nil
}

// runJevCheck performs one real TypeSafe call with candidates derived from the
// enabled registry pairs and prints the normalized result. It is the manual
// counterpart to the build-tagged integration test: it proves the wire format,
// the credential and the pinned model against the real API without putting Jev
// on the request path.
func runJevCheck(ctx context.Context, cfg config.Config, catalog *models.Catalog, prompt string, output io.Writer) error {
	printed := output
	if !cfg.Jev.Enabled {
		return errors.New("jev.enabled is false; -jev-check needs an enabled Jev configuration")
	}
	candidates, err := checkCandidates(catalog)
	if err != nil {
		return err
	}
	client, err := newJevClient(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		return err
	}
	if client == nil {
		return errors.New("jev client is not configured")
	}
	defer client.CloseIdleConnections()
	if prompt == "" {
		prompt = checkPrompt
	}
	fmt.Fprintf(printed, "REAL TYPESEAFE CALL: sending one request to %s using model %s\n", cfg.Jev.BaseURL, cfg.Jev.Model)
	fmt.Fprintf(printed, "the prompt below leaves this machine and is sent to TypeSafe: %q\n", prompt)
	fmt.Fprintf(printed, "candidates: %d\n", len(candidates))
	started := time.Now()
	result, err := client.Route(ctx, jev.Request{
		Protocol:   providers.ProtocolChatCompletions,
		Candidates: candidates,
		Messages:   []jev.Message{{Role: "user", Content: prompt}},
	})
	latency := time.Since(started)
	if err != nil {
		return fmt.Errorf("jev call failed after %s: %w", latency.Round(time.Millisecond), err)
	}
	fmt.Fprintf(printed, "\nnormalized result\n")
	fmt.Fprintf(printed, "  selected: %s\n", result.Selected)
	fmt.Fprintf(printed, "  confidence: %.6f\n", result.Confidence)
	fmt.Fprintf(printed, "  latency: %s\n", latency.Round(time.Millisecond))
	fmt.Fprintf(printed, "  probabilities:\n")
	// Output is limited to model ids and numbers: no credential, no prompt echo.
	for _, entry := range result.Probabilities {
		fmt.Fprintf(printed, "    %-32s %.6f\n", entry.Model, entry.Probability)
	}
	return nil
}

// checkCandidates derives the check's candidate list from the enabled registry
// pairs. Capability merging across providers is deliberately not done here:
// stage 7 owns that decision, and an approximation in a maintenance command
// would be indistinguishable from the real thing.
func checkCandidates(catalog *models.Catalog) ([]jev.Candidate, error) {
	if catalog == nil {
		return nil, errors.New("the model registry is empty; configure and enable a Provider/model in the WebUI before checking Jev")
	}
	seen := make(map[string]struct{})
	candidates := make([]jev.Candidate, 0, len(catalog.Pairs))
	for _, pair := range catalog.Pairs {
		if _, duplicate := seen[pair.ModelID]; duplicate {
			continue
		}
		seen[pair.ModelID] = struct{}{}
		candidates = append(candidates, jev.Candidate{
			Model:             pair.ModelID,
			ContextWindow:     pair.ContextWindow,
			MaxOutput:         pair.MaxOutput,
			SupportsTools:     pair.SupportsTools,
			SupportsVision:    pair.SupportsVision,
			SupportsReasoning: pair.SupportsReasoning,
		})
	}
	if len(candidates) == 0 {
		return nil, errors.New("the model registry has no enabled pair; configure at least one enabled registry.providers entry and model before checking Jev")
	}
	// The catalog is already sorted by the registry contract; sorting again by
	// model id keeps the check's output stable regardless of priority changes.
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].Model < candidates[j].Model })
	if len(candidates) > maxCheckCandidates {
		candidates = candidates[:maxCheckCandidates]
	}
	return candidates, nil
}

// renderJevCheckError formats a failure so it names the next action without
// echoing configuration values that may be secret.
func renderJevCheckError(err error) string {
	switch {
	case errors.Is(err, jev.ErrNotConfigured):
		return "jev is not configured: set jev.enabled, jev.base_url, jev.api_key and a pinned jev.model"
	case errors.Is(err, jev.ErrRejected):
		return "TypeSafe rejected the request: verify jev.api_key, jev.model and that the account has access"
	case errors.Is(err, jev.ErrUnavailable):
		return "TypeSafe is unreachable: verify jev.base_url and network connectivity"
	case errors.Is(err, jev.ErrTimeout):
		return "TypeSafe did not answer inside jev.timeout: raise jev.timeout if the endpoint is merely slow"
	case errors.Is(err, jev.ErrInvalidResponse):
		return "TypeSafe returned an unparseable response: the verified wire format may have changed"
	case errors.Is(err, jev.ErrInvalidResult):
		return "TypeSafe returned a result that failed validation: the recommendation is unusable and must not be trusted"
	case errors.Is(err, jev.ErrCanceled):
		return "the check was canceled"
	default:
		return err.Error()
	}
}
