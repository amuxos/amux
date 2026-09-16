package isolation

import (
	"fmt"
	"path/filepath"
	"sync"
	"testing"
)

func TestMappingStoreStableAndUnique(t *testing.T) {
	store, err := OpenMappingStore(filepath.Join(t.TempDir(), "mappings.db"), 700000)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	first, err := store.Ensure("07bedb82-018c-45dd-bd1f-c03473e512ca")
	if err != nil {
		t.Fatal(err)
	}
	again, err := store.Ensure(first.PrincipalID)
	if err != nil {
		t.Fatal(err)
	}
	if first.Account != again.Account || first.UID != again.UID {
		t.Fatalf("mapping changed: first=%+v again=%+v", first, again)
	}
	second, err := store.Ensure("38d8f1eb-b2f8-408e-8566-21988f084e31")
	if err != nil {
		t.Fatal(err)
	}
	if second.Account == first.Account || second.UID == first.UID {
		t.Fatalf("mappings collide: first=%+v second=%+v", first, second)
	}
}

func TestMappingStoreConcurrentEnsure(t *testing.T) {
	store, err := OpenMappingStore(filepath.Join(t.TempDir(), "mappings.db"), 700000)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	const workers = 12
	results := make(chan AccountMapping, workers)
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			mapping, err := store.Ensure("8f17648a-6579-42d2-afb3-df5a9e13881d")
			results <- mapping
			errs <- err
		}()
	}
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	var want string
	for mapping := range results {
		got := fmt.Sprintf("%s/%d", mapping.Account, mapping.UID)
		if want == "" {
			want = got
		} else if got != want {
			t.Fatalf("mapping = %s, want %s", got, want)
		}
	}
}

func TestMappingStoreConcurrentEnsureAcrossConnections(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mappings.db")
	firstStore, err := OpenMappingStoreRange(path, 700000, 700010)
	if err != nil {
		t.Fatal(err)
	}
	defer firstStore.Close()
	secondStore, err := OpenMappingStoreRange(path, 700000, 700010)
	if err != nil {
		t.Fatal(err)
	}
	defer secondStore.Close()

	principalIDs := []string{
		"07bedb82-018c-45dd-bd1f-c03473e512ca",
		"38d8f1eb-b2f8-408e-8566-21988f084e31",
	}
	results := make(chan AccountMapping, len(principalIDs))
	errs := make(chan error, len(principalIDs))
	var wg sync.WaitGroup
	for i, store := range []*MappingStore{firstStore, secondStore} {
		wg.Add(1)
		go func(store *MappingStore, principalID string) {
			defer wg.Done()
			mapping, err := store.Ensure(principalID)
			results <- mapping
			errs <- err
		}(store, principalIDs[i])
	}
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	seenUIDs := make(map[int]bool)
	for mapping := range results {
		if seenUIDs[mapping.UID] {
			t.Fatalf("duplicate uid allocated across connections: %d", mapping.UID)
		}
		seenUIDs[mapping.UID] = true
	}
}

func TestMappingStoreEnforcesConfiguredUIDRange(t *testing.T) {
	store, err := OpenMappingStoreRange(filepath.Join(t.TempDir(), "mappings.db"), 700000, 700000)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.Ensure("07bedb82-018c-45dd-bd1f-c03473e512ca"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Ensure("38d8f1eb-b2f8-408e-8566-21988f084e31"); err == nil {
		t.Fatal("second mapping succeeded after configured UID range was exhausted")
	}
}
