package models

import (
	"sync"
	"sync/atomic"
)

// Store holds the current catalog snapshot. Readers use Load without locking;
// writers use Swap, which serialises generation assignment so a snapshot's
// generation is monotonic and never observed out of order.
type Store struct {
	mutex      sync.Mutex
	generation uint64
	current    atomic.Pointer[Catalog]
}

// NewStore returns a store holding an empty catalog, so Load never returns nil
// and callers cannot panic on a process that has not loaded the registry yet.
func NewStore() *Store {
	store := &Store{}
	store.current.Store(&Catalog{})
	return store
}

// Swap publishes a catalog and returns it with its assigned generation. The
// caller must not mutate a catalog after publishing it.
func (s *Store) Swap(catalog *Catalog) *Catalog {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	s.generation++
	catalog.Generation = s.generation
	s.current.Store(catalog)
	return catalog
}

// SwapWith publishes a catalog like Swap, but first calls prepare with the
// catalog after its generation is assigned and before any reader can observe it.
// It is how state derived from a catalog (the provider executor set) is made
// available no later than the catalog itself, so a request can never see a
// generation whose derived state does not exist yet.
func (s *Store) SwapWith(catalog *Catalog, prepare func(*Catalog)) *Catalog {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	s.generation++
	catalog.Generation = s.generation
	if prepare != nil {
		prepare(catalog)
	}
	s.current.Store(catalog)
	return catalog
}

// Load returns the current snapshot. The result is never nil.
func (s *Store) Load() *Catalog {
	return s.current.Load()
}
