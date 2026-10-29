//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	appreference "github.com/proshop-app/proshop-marketplace/internal/application/reference"
	"github.com/proshop-app/proshop-marketplace/internal/domain/reference"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/postgres"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/random"
)

const (
	referenceDict = "b1"
	// insertTestDictionary — нейтральный словарь теста рядом со словарём миграции.
	insertTestDictionary = `
		INSERT INTO reference.dictionaries (code, kind, identity, alias_kinds, limits)
		SELECT 'b1', kind, identity, alias_kinds, limits
		FROM reference.dictionaries
		WHERE code = 'brand'`
	countReferenceTasks   = `SELECT count(*) FROM outbox WHERE kind = 'reference.changed'`
	countReferenceChanges = `SELECT count(*) FROM reference.changes WHERE dictionary_code = 'b1'`
	// largeDelivery — записей в поставке теста пачек: десять пачек по 500
	largeDelivery = 5000
)

var referenceMeta = reference.Meta{Actor: "t1", Source: reference.SourceOperator}

type referenceClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *referenceClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *referenceClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func referenceService(t *testing.T) (*appreference.Service, *pgxpool.Pool, *referenceClock) {
	t.Helper()
	pool := testPool(t)
	if _, err := pool.Exec(context.Background(), insertTestDictionary); err != nil {
		t.Fatal(err)
	}
	clock := &referenceClock{now: time.Now().UTC()}
	service := appreference.NewService(postgres.NewUnitOfWork(pool), clock, random.IDs{}, nil, appreference.Config{})
	return service, pool, clock
}

func createReference(
	t *testing.T,
	service *appreference.Service,
	name string,
	status reference.Status,
	aliases ...string,
) appreference.EntryResult {
	t.Helper()
	specs := make([]appreference.AliasSpec, 0, len(aliases))
	for _, alias := range aliases {
		specs = append(specs, appreference.AliasSpec{Text: alias})
	}
	result, err := service.Create(context.Background(), appreference.CreateCommand{
		Dictionary: referenceDict,
		Name:       name,
		Status:     status,
		Aliases:    specs,
		Meta:       referenceMeta,
	})
	if err != nil {
		t.Fatalf("Create(%s): %v", name, err)
	}
	return result
}

func TestReferenceMigrationSeedsOpenIdentityDictionary(t *testing.T) {
	pool := testPool(t)
	var dict *reference.Dictionary
	err := postgres.NewUnitOfWork(pool).Do(context.Background(), func(ctx context.Context, tx ports.Tx) error {
		var err error
		dict, err = tx.Reference().Dictionary(ctx, "brand")
		return err
	})
	state := dict.State()
	if err != nil || state.Kind != reference.KindOpen || !state.Identity || dict.CheckNormalizer() != nil ||
		state.Limits != reference.DefaultLimits() || len(state.AliasKinds) != len(reference.AliasKinds) {
		t.Fatalf("словарь миграции: %+v %v", state, err)
	}
}

func TestReferenceScenariosOnPostgres(t *testing.T) {
	service, pool, _ := referenceService(t)
	ctx := context.Background()
	first := createReference(t, service, "Бренд", reference.StatusConfirmed, "Brend")
	if first.Revision != first.Entry.Revision || len(first.Entry.Aliases) != 2 {
		t.Fatalf("создание: %+v", first)
	}
	_, err := service.Create(ctx, appreference.CreateCommand{
		Dictionary: referenceDict,
		Name:       "BREND",
		Status:     reference.StatusConfirmed,
		Meta:       referenceMeta,
	})
	if !errors.Is(err, reference.ErrKeyTaken) {
		t.Fatalf("занятый ключ: %v", err)
	}
	candidate := createReference(t, service, "Брендовый", reference.StatusCandidate)
	again := createReference(t, service, "БРЕНДОВЫЙ", reference.StatusCandidate)
	view, err := service.Get(ctx, referenceDict, candidate.Entry.ID)
	if err != nil || again.Created || view.Entry.Occurrences != 2 {
		t.Fatalf("встречи кандидата: %+v %v", view.Entry, err)
	}

	fuzzy, err := service.Resolve(ctx, referenceDict, "бренд")
	if err != nil || fuzzy.Match == nil || fuzzy.Match.EntryID != first.Entry.ID {
		t.Fatalf("точное совпадение: %+v %v", fuzzy, err)
	}
	similar, err := service.Resolve(ctx, referenceDict, "брэнд")
	if err != nil || similar.Match != nil || len(similar.Candidates) == 0 {
		t.Fatalf("кандидаты по триграммам: %+v %v", similar, err)
	}
	prefix, err := service.Resolve(ctx, referenceDict, "бре")
	if err != nil || prefix.Match != nil || len(prefix.Candidates) != 2 ||
		!strings.HasPrefix(prefix.Candidates[0].AliasKey, "бре") {
		t.Fatalf("кандидаты по префиксу: %+v %v", prefix, err)
	}

	second := createReference(t, service, "Бренд Два", reference.StatusConfirmed)
	merged, err := service.Merge(ctx, appreference.MergeCommand{
		Dictionary: referenceDict,
		ID:         first.Entry.ID,
		Into:       second.Entry.ID,
		IfMatch:    first.Entry.Revision,
		Meta:       referenceMeta,
	})
	if err != nil || len(merged.Entry.Aliases) != 3 {
		t.Fatalf("объединение: %+v %v", merged, err)
	}
	resolved, err := service.Resolve(ctx, referenceDict, "brend")
	if err != nil || resolved.Match == nil || resolved.Match.EntryID != second.Entry.ID {
		t.Fatalf("вариант после объединения: %+v %v", resolved, err)
	}
	view, err = service.Get(ctx, referenceDict, first.Entry.ID)
	if err != nil || view.Entry.Status != reference.StatusMerged || view.Resolved == nil ||
		view.Resolved.ID != second.Entry.ID {
		t.Fatalf("объединённая запись: %+v %v", view, err)
	}
	_, err = service.Merge(ctx, appreference.MergeCommand{
		Dictionary: referenceDict,
		ID:         second.Entry.ID,
		Into:       first.Entry.ID,
		IfMatch:    second.Entry.Revision,
		Meta:       referenceMeta,
	})
	if !errors.Is(err, reference.ErrEntryChanged) {
		t.Fatalf("устаревший If-Match: %v", err)
	}

	full, err := service.Snapshot(ctx, referenceDict, nil)
	if err != nil || !full.Full || len(full.Entries) != 3 || full.Revision != merged.Revision {
		t.Fatalf("полный снимок: %+v %v", full, err)
	}
	since := second.Revision
	delta, err := service.Snapshot(ctx, referenceDict, &since)
	if err != nil || delta.Full || len(delta.Entries) != 2 {
		t.Fatalf("дельта после объединения: %+v %v", delta, err)
	}
	var tasks int
	if err := pool.QueryRow(ctx, countReferenceTasks).Scan(&tasks); err != nil || tasks != 4 {
		t.Fatalf("событий в outbox: %d %v", tasks, err)
	}
	if _, err := pool.Exec(ctx, "UPDATE reference.changes SET reason = 'x'"); err == nil {
		t.Fatal("журнал изменён на месте")
	}
	if _, err := pool.Exec(ctx, "DELETE FROM reference.changes"); err == nil {
		t.Fatal("журнал удалён")
	}
}

func TestReferenceSnapshotHorizon(t *testing.T) {
	service, _, clock := referenceService(t)
	ctx := context.Background()
	createReference(t, service, "Бренд", reference.StatusConfirmed)
	clock.advance(31 * 24 * time.Hour)
	second := createReference(t, service, "Бренд Два", reference.StatusConfirmed)
	old := int64(0)
	aged, err := service.Snapshot(ctx, referenceDict, &old)
	if err != nil || !aged.Full {
		t.Fatalf("since за горизонтом: %+v %v", aged, err)
	}
	inside := second.Revision - 1
	delta, err := service.Snapshot(ctx, referenceDict, &inside)
	if err != nil || delta.Full || len(delta.Entries) != 1 {
		t.Fatalf("since на горизонте: %+v %v", delta, err)
	}
}

func TestReferenceDeliveryOnPostgres(t *testing.T) {
	service, _, _ := referenceService(t)
	ctx := context.Background()
	existing := createReference(t, service, "Бренд", reference.StatusCandidate)
	aliases := []reference.DeliveryAlias{{Text: "Brend", Kind: reference.AliasTranslit}}
	delivery := reference.Delivery{
		Dictionary: referenceDict,
		Version:    1,
		Checksum:   strings.Repeat("a", 64),
		Entries: []reference.DeliveryEntry{
			{Canonical: "Бренд", Status: reference.StatusConfirmed, Aliases: aliases},
			{Canonical: "Бренд Два", Status: reference.StatusConfirmed, Metadata: []byte(`{"directions": ["d1"]}`)},
		},
	}
	applied, err := service.Apply(ctx, delivery)
	if err != nil || applied.Created != 1 || applied.Updated != 1 {
		t.Fatalf("поставка: %+v %v", applied, err)
	}
	repeat, err := service.Apply(ctx, delivery)
	if err != nil || repeat.Changed || repeat.Revision != applied.Revision {
		t.Fatalf("повтор поставки: %+v %v", repeat, err)
	}
	rolled, err := service.Rollback(ctx, delivery)
	if err != nil || !rolled.Changed {
		t.Fatalf("откат: %+v %v", rolled, err)
	}
	snapshot, err := service.Snapshot(ctx, referenceDict, nil)
	if err != nil || len(snapshot.Entries) != 1 || snapshot.Entries[0].ID != existing.Entry.ID ||
		snapshot.Entries[0].Status != reference.StatusCandidate || len(snapshot.Entries[0].Aliases) != 1 {
		t.Fatalf("после отката: %+v %v", snapshot, err)
	}
	if again, err := service.Rollback(ctx, delivery); err != nil || again.Changed {
		t.Fatalf("повтор отката: %+v %v", again, err)
	}
	if _, err := service.Apply(ctx, delivery); !errors.Is(err, reference.ErrDeliveryConflict) {
		t.Fatalf("повтор откаченной поставки: %v", err)
	}
}

func TestReferenceConcurrentClaimsOfOneKey(t *testing.T) {
	service, _, _ := referenceService(t)
	ctx := context.Background()
	var group sync.WaitGroup
	failures := make(chan error, 4)
	for i := 0; i < 4; i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			_, err := service.Create(ctx, appreference.CreateCommand{
				Dictionary: referenceDict,
				Name:       "Бренд",
				Status:     reference.StatusConfirmed,
				Meta:       referenceMeta,
			})
			failures <- err
		}()
	}
	group.Wait()
	close(failures)
	created := 0
	for err := range failures {
		switch {
		case err == nil:
			created++
		case !errors.Is(err, reference.ErrKeyTaken):
			t.Fatalf("второй претендент: %v", err)
		}
	}
	if created != 1 {
		t.Fatalf("ключ получили %d записей", created)
	}
}

func TestReferenceConditionalSaveAndPresence(t *testing.T) {
	service, pool, _ := referenceService(t)
	ctx := context.Background()
	entry := createReference(t, service, "Бренд", reference.StatusCandidate)
	uow := postgres.NewUnitOfWork(pool)
	var stale *reference.Entry
	err := uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		var err error
		stale, err = tx.Reference().Entry(ctx, referenceDict, entry.Entry.ID)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	createReference(t, service, "бренд", reference.StatusCandidate)
	stale.Meet(time.Now())
	err = uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		return tx.Reference().Save(ctx, []*reference.Entry{stale}, nil)
	})
	if !errors.Is(err, reference.ErrEntryChanged) {
		t.Fatalf("сохранение поверх чужой версии: %v", err)
	}

	rows := []reference.Presence{{CategoryID: "c1", Offers: 3, Sellers: 2}, {CategoryID: "c2", Offers: 1, Sellers: 1}}
	for _, replace := range [][]reference.Presence{rows, rows[:1]} {
		err = uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
			return tx.Reference().ReplacePresence(ctx, entry.Entry.ID, replace, time.Now())
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	var count int
	err = pool.QueryRow(ctx, "SELECT count(*) FROM reference.entry_presence").Scan(&count)
	if err != nil || count != 1 {
		t.Fatalf("присутствие не заменилось целиком: %d %v", count, err)
	}
	err = uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		return tx.Reference().ReplacePresence(ctx, random.IDs{}.NewUUID(), rows, time.Now())
	})
	if !errors.Is(err, reference.ErrEntryNotFound) {
		t.Fatalf("присутствие неизвестной записи: %v", err)
	}
}

func TestReferenceLargeDeliveryWritesRevisionPerBatch(t *testing.T) {
	service, pool, _ := referenceService(t)
	ctx := context.Background()
	entries := make([]reference.DeliveryEntry, 0, largeDelivery)
	for i := 0; i < largeDelivery; i++ {
		aliases := []reference.DeliveryAlias{{Text: "b" + strconv.Itoa(i) + " x"}}
		entries = append(entries, reference.DeliveryEntry{
			Canonical: "b" + strconv.Itoa(i),
			Status:    reference.StatusConfirmed,
			Aliases:   aliases,
		})
	}
	delivery := reference.Delivery{
		Dictionary: referenceDict,
		Version:    1,
		Checksum:   strings.Repeat("c", 64),
		Entries:    entries,
	}
	started := time.Now()
	applied, err := service.Apply(ctx, delivery)
	if err != nil || applied.Created != largeDelivery {
		t.Fatalf("поставка: %+v %v", applied, err)
	}
	t.Logf("поставка %d записей: %s", largeDelivery, time.Since(started))
	var changes int
	if err := pool.QueryRow(ctx, countReferenceChanges).Scan(&changes); err != nil || changes != largeDelivery/500 {
		t.Fatalf("ревизий журнала: %d %v", changes, err)
	}
	started = time.Now()
	rolled, err := service.Rollback(ctx, delivery)
	if err != nil || rolled.Updated != largeDelivery {
		t.Fatalf("откат: %+v %v", rolled, err)
	}
	t.Logf("откат %d записей: %s", largeDelivery, time.Since(started))
	snapshot, err := service.Snapshot(ctx, referenceDict, nil)
	if err != nil || len(snapshot.Entries) != 0 {
		t.Fatalf("после отката: %d записей %v", len(snapshot.Entries), err)
	}
}
