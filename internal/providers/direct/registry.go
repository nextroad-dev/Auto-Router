package direct

import (
	"sync/atomic"

	"github.com/nextroad-dev/Auto-Router/internal/models"
	"github.com/nextroad-dev/Auto-Router/internal/providers"
	"github.com/nextroad-dev/Auto-Router/internal/providers/adapter"
)

// Registry holds the executor set for one catalog generation. Reads are
// lock-free; a rebuild swaps the complete set in one atomic operation.
type Registry struct {
	current atomic.Pointer[snapshot]
}

type snapshot struct {
	generation uint64
	executors  map[string]providers.Executor
}

// NewRegistry returns a registry with an empty, non-nil snapshot.
func NewRegistry() *Registry {
	registry := &Registry{}
	registry.current.Store(&snapshot{executors: map[string]providers.Executor{}})
	return registry
}

// Build constructs executors from catalog and atomically publishes the new
// snapshot. Providers without a base URL or with an invalid endpoint are skipped
// so one unusable row cannot take healthy providers offline. Existing idle pools
// are closed after the swap; in-flight requests retain their old executor.
func (r *Registry) Build(catalog *models.Catalog) {
	if r == nil {
		return
	}
	newExecutors := map[string]providers.Executor{}
	var generation uint64
	if catalog != nil {
		newExecutors = make(map[string]providers.Executor, len(catalog.Providers))
		generation = catalog.Generation
		for _, provider := range catalog.Providers {
			if provider.BaseURL == "" {
				continue
			}
			kind := provider.Kind
			if kind == "" {
				kind = models.ProviderOpenAICompatible
			}
			if !kind.Valid() {
				continue
			}
			executor, err := New(Config{
				BaseURL:     provider.BaseURL,
				APIKey:      provider.APIKey,
				ProviderKey: provider.Key,
				Kind:        kind,
			})
			if err != nil {
				continue
			}
			adapted, err := adapter.New(kind, executor)
			if err != nil {
				executor.CloseIdleConnections()
				continue
			}
			newExecutors[provider.Key] = adapted
		}
	}
	old := r.current.Swap(&snapshot{generation: generation, executors: newExecutors})
	if old == nil {
		return
	}
	for _, executor := range old.executors {
		if closer, ok := executor.(interface{ CloseIdleConnections() }); ok {
			closer.CloseIdleConnections()
		}
	}
}

// Get returns the executor selected for one request. The caller should resolve
// it once and retain the returned interface for the request lifetime.
func (r *Registry) Get(providerKey string) (providers.Executor, bool) {
	if r == nil {
		return nil, false
	}
	snap := r.current.Load()
	if snap == nil {
		return nil, false
	}
	executor, ok := snap.executors[providerKey]
	return executor, ok
}

// Generation reports the catalog generation represented by the current snapshot.
func (r *Registry) Generation() uint64 {
	if r == nil {
		return 0
	}
	snap := r.current.Load()
	if snap == nil {
		return 0
	}
	return snap.generation
}
