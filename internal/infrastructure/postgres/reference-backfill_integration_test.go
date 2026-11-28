//go:build integration

package postgres_test

import (
	"context"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	appreference "github.com/proshop-app/proshop-marketplace/internal/application/reference"
	"github.com/proshop-app/proshop-marketplace/internal/domain/reference"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/postgres"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/random"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

const (
	legacySnapshotVersions          = "TRUNCATE reference.snapshot_versions RESTART IDENTITY"
	snapshotBackfillPreviousVersion = 84
	backfillEntryCount              = 1370
)

func TestReferenceSnapshotBackfill(t *testing.T) {
	ctx := context.Background()
	service, pool, clock := referenceService(t)
	delivery := seedReferenceBackfill(t, service, pool)
	// Обычная запись после 00076 уже создала версию. Она должна остаться на прежней ревизии.
	live := createReference(t, service, "Запись после 00076", reference.StatusConfirmed)
	before, err := service.Dictionaries(ctx)
	if err != nil {
		t.Fatal(err)
	}
	old, err := service.Snapshot(ctx, referenceDict, nil, "")
	if err != nil || len(old.Entries) != 1 || old.Entries[0].ID != live.Entry.ID {
		t.Fatalf("ожидался неполный снимок до восстановления: %+v, %v", old, err)
	}
	if err := postgres.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}

	config := pool.Config().Copy()
	counter := &statementCounter{}
	config.ConnConfig.Tracer = counter
	traced, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer traced.Close()
	reader := appreference.NewService(postgres.NewUnitOfWork(traced), clock, random.IDs{}, nil, appreference.Config{})
	after, err := reader.Dictionaries(ctx)
	if err != nil || counter.statements.Load() != 3 {
		t.Fatalf("discovery: SQL=%d, %v", counter.statements.Load(), err)
	}
	assertBackfilledDictionaries(t, reader, pool, before, after, counter)

	since := old.Revision
	delta, err := databaseSnapshot(reader, &since)
	if err != nil || delta.Full || len(delta.Entries) != backfillEntryCount-1 || len(delta.Removed) != 0 {
		t.Fatalf("дельта для тёплого кеша: full=%v, entries=%d, %v", delta.Full, len(delta.Entries), err)
	}
	for _, entry := range delta.Entries {
		if entry.ID == live.Entry.ID || entry.Revision <= since {
			t.Fatalf("восстановление переписало актуальный образ или старую ревизию: %+v", entry)
		}
	}

	if err := postgres.MigrateDownTo(ctx, pool, snapshotBackfillPreviousVersion); err != nil {
		t.Fatal(err)
	}
	if err := postgres.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	again, err := reader.Dictionaries(ctx)
	if err != nil || !slices.Equal(again, after) {
		t.Fatalf("повтор миграции поменял головы: %v, %v", again, err)
	}
	rolled, err := service.Rollback(ctx, delivery)
	if err != nil || !rolled.Changed || rolled.Updated != 2 {
		t.Fatalf("восстановление образов помешало откату поставки: %+v, %v", rolled, err)
	}
}

func seedReferenceBackfill(t *testing.T, service *appreference.Service, pool *pgxpool.Pool) reference.Delivery {
	t.Helper()
	ctx := context.Background()
	large := reference.Delivery{
		Dictionary: referenceDict,
		Version:    1,
		Checksum:   strings.Repeat("a", 64),
	}
	for i := range backfillEntryCount - 1 {
		large.Entries = append(large.Entries, reference.DeliveryEntry{
			Canonical: "Значение " + strconv.Itoa(i),
			Status:    reference.StatusConfirmed,
		})
	}
	large.Entries[0].Metadata = []byte(`{"directions":["d1"],"order":7,"label":"Порода","extra":null}`)
	if _, err := service.Apply(ctx, large); err != nil {
		t.Fatal(err)
	}
	mixed := reference.Delivery{
		Dictionary: "material",
		Version:    1,
		Checksum:   strings.Repeat("b", 64),
		Entries: []reference.DeliveryEntry{
			{
				Canonical: "Дерево",
				Status:    reference.StatusConfirmed,
				Aliases: []reference.DeliveryAlias{
					{Text: "Wood", Kind: reference.AliasLocale, Locale: "en"},
				},
			},
			{Canonical: "Бамбук", Status: reference.StatusCandidate},
		},
	}
	if _, err := service.Apply(ctx, mixed); err != nil {
		t.Fatal(err)
	}
	// Воспроизводим пустую таблицу 00076 при уже заполненном реестре и журнале.
	if err := postgres.MigrateDownTo(ctx, pool, snapshotBackfillPreviousVersion); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, legacySnapshotVersions); err != nil {
		t.Fatal(err)
	}
	return mixed
}

func assertBackfilledDictionaries(
	t *testing.T,
	reader *appreference.Service,
	pool *pgxpool.Pool,
	before, after []appreference.DictionaryView,
	counter *statementCounter,
) {
	t.Helper()
	if len(after) != len(before) {
		t.Fatalf("набор словарей изменился: %d → %d", len(before), len(after))
	}
	for i, dictionary := range after {
		old := before[i]
		repaired := dictionary.Code == referenceDict || dictionary.Code == "material"
		if dictionary.Code != old.Code || (repaired && dictionary.Revision <= old.Revision) ||
			(!repaired && dictionary.Revision != old.Revision) {
			t.Fatalf("неверная голова: %+v → %+v", old, dictionary)
		}
		if repaired {
			assertReferenceTask(t, pool, dictionary.Code, 0, dictionary.Revision, true)
		}
		assertBackfilledDictionary(t, reader, pool, dictionary, counter)
	}
}

func assertBackfilledDictionary(
	t *testing.T,
	reader *appreference.Service,
	pool *pgxpool.Pool,
	dictionary appreference.DictionaryView,
	counter *statementCounter,
) {
	t.Helper()
	ctx := context.Background()
	want := map[ids.UUID]reference.EntryState{}
	err := postgres.NewUnitOfWork(pool).Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		entries, err := tx.Reference().ConfirmedEntries(ctx, dictionary.Code)
		for _, entry := range entries {
			want[entry.ID()] = entry.State()
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	start := counter.statements.Load()
	pages := 0
	for cursor := ""; ; {
		page, err := reader.Snapshot(ctx, dictionary.Code, nil, cursor)
		pages++
		if err != nil || !page.Full || page.Revision != dictionary.Revision || len(page.Removed) != 0 {
			t.Fatalf("%s: неверная страница %d: %v", dictionary.Code, pages, err)
		}
		for _, entry := range page.Entries {
			expected, found := want[entry.ID]
			entry.Metadata, err = reference.CleanMetadata(entry.Metadata)
			if err != nil || !found || !entry.CreatedAt.Equal(expected.CreatedAt) ||
				!reference.SameContent(&entry, &expected) {
				t.Fatalf(
					"%s: снимок не совпал с реестром: %+v, ожидалось %+v, %v",
					dictionary.Code,
					entry,
					expected,
					err,
				)
			}
			delete(want, entry.ID)
		}
		cursor = page.NextCursor
		if cursor == "" {
			break
		}
	}
	if len(want) != 0 {
		t.Fatalf("%s: нет %d подтверждённых записей", dictionary.Code, len(want))
	}
	if sql := counter.statements.Load() - start; dictionary.Code == referenceDict && (pages != 11 || sql != 44) {
		t.Fatalf("обход %d записей: %d страниц, %d SQL", backfillEntryCount, pages, sql)
	}
}
