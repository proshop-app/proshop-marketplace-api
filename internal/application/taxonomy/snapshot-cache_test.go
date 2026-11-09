package taxonomy

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
)

type countingTaxonomy struct {
	taxonomy.Repository
	reads   atomic.Int64
	entered chan struct{}
	release chan struct{}
}

type snapshotTx struct {
	ports.Tx
	repo taxonomy.Repository
}

func (tx snapshotTx) Taxonomy() taxonomy.Repository { return tx.repo }

func (r *countingTaxonomy) Version(_ context.Context, version int64) (taxonomy.Definition, error) {
	r.reads.Add(1)
	if r.entered != nil {
		r.entered <- struct{}{}
		<-r.release
	}
	return taxonomy.Definition{Version: version}, nil
}

func TestSnapshotCacheSurvivesTransactionsAndEvictsLRU(t *testing.T) {
	store := fake.NewStore()
	for version := int64(1); version <= snapshotCacheLimit+1; version++ {
		store.SeedTaxonomy(taxonomy.Definition{Version: version})
	}
	service := NewService(store, nil, nil, 0)
	read := func(version int64) {
		t.Helper()
		if err := store.Do(context.Background(), func(ctx context.Context, tx ports.Tx) error {
			d, err := service.snapshot(ctx, tx, version)
			if err == nil && d.Version != version {
				t.Fatalf("снимок %d вместо %d", d.Version, version)
			}
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	for version := int64(1); version <= snapshotCacheLimit; version++ {
		read(version)
	}
	read(1)
	seen := map[int64]bool{}
	for _, entry := range service.snapshots.entries {
		if seen[entry.Version] {
			t.Fatal("попадание в кэш создало дубликат версии")
		}
		seen[entry.Version] = true
	}
	if len(service.snapshots.entries) != snapshotCacheLimit || service.snapshots.entries[0].Version != 1 {
		t.Fatal("кэш не пережил транзакции или не обновил LRU")
	}
	read(snapshotCacheLimit + 1)
	if len(service.snapshots.entries) != snapshotCacheLimit {
		t.Fatal("кэш превысил лимит")
	}
	for _, entry := range service.snapshots.entries {
		if entry.Version == 2 {
			t.Fatal("давно не использованный снимок не вытеснен")
		}
	}
}

func TestSnapshotCacheHitDoesNotReadStorage(t *testing.T) {
	repo := &countingTaxonomy{}
	service := NewService(nil, nil, nil, 0)
	for i := 0; i < 3; i++ {
		if _, err := service.snapshot(context.Background(), snapshotTx{repo: repo}, 1); err != nil {
			t.Fatal(err)
		}
	}
	if got := repo.reads.Load(); got != 1 {
		t.Fatalf("чтений Version: %d, ожидалось 1", got)
	}
}

func TestSnapshotCacheConcurrentMissHasNoDuplicates(t *testing.T) {
	const workers = 24
	repo := &countingTaxonomy{entered: make(chan struct{}, workers), release: make(chan struct{})}
	service := NewService(nil, nil, nil, 0)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := service.snapshot(context.Background(), snapshotTx{repo: repo}, 1); err != nil {
				t.Error(err)
			}
		}()
	}
	// Все чтения промахнулись до первой вставки: тест не зависит от задержек планировщика.
	for i := 0; i < workers; i++ {
		<-repo.entered
	}
	close(repo.release)
	wg.Wait()
	if len(service.snapshots.entries) != 1 {
		t.Fatal("дубликаты одной версии")
	}
	repo.entered = nil
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(version int64) {
			defer wg.Done()
			if _, err := service.snapshot(context.Background(), snapshotTx{repo: repo}, version); err != nil {
				t.Error(err)
			}
		}(int64(i%20 + 1))
	}
	wg.Wait()
	seen := map[int64]bool{}
	for _, entry := range service.snapshots.entries {
		if seen[entry.Version] {
			t.Fatal("дубликат версии")
		}
		seen[entry.Version] = true
	}
	if len(seen) > snapshotCacheLimit {
		t.Fatal("превышен лимит кэша")
	}
}

func TestSnapshotPoliciesInheritDomainDefault(t *testing.T) {
	d := taxonomy.Definition{Categories: []taxonomy.Category{
		{ID: "default"},
		{ID: "root", Moderation: "manual", Children: []taxonomy.Category{
			{ID: "inherited"},
			{ID: "override", Moderation: "forbidden"},
		}},
	}}
	snapshot := policySnapshot{Policies: categoryPolicies(d)}
	for _, id := range []string{"default", "root", "inherited", "override", "missing"} {
		if snapshot.policy(id) != d.ModerationPolicy(id) {
			t.Fatalf("неверная политика %s", id)
		}
	}
}
