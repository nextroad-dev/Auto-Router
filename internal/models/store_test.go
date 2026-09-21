package models

import (
	"sync"
	"testing"
)

func TestStoreStartsWithEmptyCatalogAndIncrementsGeneration(t *testing.T) {
	store := NewStore()
	if catalog := store.Load(); catalog == nil || !catalog.Empty() || catalog.Generation != 0 {
		t.Fatalf("new store should hold an empty generation-0 catalog, got %+v", catalog)
	}
	first := mustCatalog(t, []Provider{provider("a", 0, true)}, []Model{model("m", 0, true)}, []Pair{pair("a", "m", 0, true)})
	if published := store.Swap(first); published.Generation != 1 {
		t.Fatalf("first generation = %d, want 1", published.Generation)
	}
	if store.Load() != first {
		t.Fatal("Load should return the published snapshot")
	}
	second := mustCatalog(t, nil, nil, nil)
	if published := store.Swap(second); published.Generation != 2 {
		t.Fatalf("second generation = %d, want 2", published.Generation)
	}
	if store.Load() != second || store.Load().Generation != 2 {
		t.Fatalf("Load should return the latest snapshot, got %+v", store.Load())
	}
}

func TestStoreConcurrentSwapsAssignDistinctGenerations(t *testing.T) {
	store := NewStore()
	const writers = 16
	var wait sync.WaitGroup
	generations := make(chan uint64, writers)
	for i := 0; i < writers; i++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			catalog := mustCatalog(t, nil, nil, nil)
			generations <- store.Swap(catalog).Generation
		}()
	}
	wait.Wait()
	close(generations)
	seen := map[uint64]bool{}
	for generation := range generations {
		if generation == 0 || generation > writers || seen[generation] {
			t.Fatalf("generation %d is out of range or duplicated: %v", generation, seen)
		}
		seen[generation] = true
	}
	if len(seen) != writers || store.Load().Generation != writers {
		t.Fatalf("expected %d distinct generations, got %v (current %d)", writers, seen, store.Load().Generation)
	}
}
