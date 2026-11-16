package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"gorm.io/gorm"

	appcatalog "github.com/proshop-app/proshop-marketplace/internal/application/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	appmoderation "github.com/proshop-app/proshop-marketplace/internal/application/moderation"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/domain/moderation"
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
)

// Только снимки идут через SQL-драйвер; прочие агрегаты — через штатный fake приложения.
type snapshotProductUOW struct {
	orm       *gorm.DB
	snapshots *Snapshots
	store     *fake.Store
	legacy    bool
}

type snapshotProductTx struct {
	ports.Tx
	scope  *scope
	legacy bool
}

type legacyTaxonomy struct {
	taxonomy.Repository
	repository *Taxonomy
}

type legacyModerationRules struct {
	moderation.RulesRepository
	repository *ModerationRules
}

func (u *snapshotProductUOW) Do(ctx context.Context, run func(context.Context, ports.Tx) error) error {
	return snapshotTransaction(u.orm, u.snapshots, func(s *scope) error {
		return u.store.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
			return run(ctx, snapshotProductTx{Tx: tx, scope: s, legacy: u.legacy})
		})
	})
}

func (s snapshotProductTx) Taxonomy() taxonomy.Repository {
	repository := s.scope.Taxonomy()
	if s.legacy {
		return legacyTaxonomy{Repository: repository, repository: s.scope.taxonomy}
	}
	return repository
}

func (s snapshotProductTx) ModerationRules() moderation.RulesRepository {
	repository := s.scope.ModerationRules()
	if s.legacy {
		return legacyModerationRules{RulesRepository: repository, repository: s.scope.moderation}
	}
	return repository
}

// Старый путь: определение и Unmarshal при каждом Current.
func (r legacyTaxonomy) Current(ctx context.Context) (taxonomy.Definition, error) {
	return r.repository.load(ctx)
}

func (r legacyModerationRules) Current(ctx context.Context) (moderation.Rules, error) {
	var revision int64
	if err := rawSQL(ctx, r.repository.db, readModerationRules).Row().Scan(&revision); err != nil {
		return moderation.Rules{}, err
	}
	return r.repository.load(ctx, revision)
}

func snapshotProductEvent(revision int64) appcatalog.ProductEvent {
	return appcatalog.ProductEvent{
		EventID: fmt.Sprintf("e%d", revision),
		Snapshot: catalog.Snapshot{
			TenantID:  "t1",
			ProductID: "p1",
			Revision:  revision,
			At:        time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
			Card: catalog.Card{
				Name:    "Товар",
				Visible: true,
				Marketplace: &taxonomy.Card{
					CategoryID: "c1",
					Published:  true,
					Attributes: map[string]json.RawMessage{"a1": json.RawMessage(`"v1"`)},
				},
			},
		},
	}
}

func largeSnapshot() taxonomy.Definition {
	d := snapshotDefinition(1)
	for index := 2; index < 3555; index++ {
		category := d.Categories[0].Children[0]
		category.ID = fmt.Sprintf("c%d", index)
		category.Slug = category.ID
		d.Categories[0].Children = append(d.Categories[0].Children, category)
	}
	return d
}

func snapshotProductPipeline(
	t testing.TB,
	legacy bool,
) (*snapshotDatabase, *appcatalog.Projector, *appcatalog.Indexer, *snapshotProductUOW) {
	t.Helper()
	orm, database := snapshotORM(t, snapshotJSON(t, largeSnapshot()))
	uow := &snapshotProductUOW{
		orm:       orm,
		snapshots: &Snapshots{},
		store:     fake.NewStore(),
		legacy:    legacy,
	}
	uow.store.SeedParticipant(participant.State{
		TenantID: "t1",
		Slug:     "t1",
		Name:     "Магазин",
		Stage:    participant.StageConnected,
		Trust:    participant.Trusted,
		Version:  1,
	})
	clock := fake.NewClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	evaluator := appmoderation.NewService(uow, clock, &fake.IDs{}, appmoderation.Config{})
	projector := appcatalog.NewProjector(uow, clock, evaluator)
	indexer := appcatalog.NewIndexer(
		uow,
		fake.NewIndex(),
		clock,
		100,
		nil,
		appcatalog.NewModelKeys(nil, nil, true),
		time.Second,
	)
	// Прогрев не входит в измерение: версия на последующих событиях не меняется.
	if err := snapshotTransaction(orm, uow.snapshots, readSnapshots); err != nil {
		t.Fatal(err)
	}
	database.reset()
	return database, projector, indexer, uow
}

func TestApplyProductSnapshotReadVolume(t *testing.T) {
	for _, legacy := range []bool{true, false} {
		t.Run(fmt.Sprintf("legacy=%v", legacy), func(t *testing.T) {
			database, projector, indexer, uow := snapshotProductPipeline(t, legacy)
			ctx := context.Background()
			if err := projector.ApplyProduct(ctx, snapshotProductEvent(1)); err != nil {
				t.Fatal(err)
			}
			reads, volume := database.readVolume()
			t.Logf(
				"ApplyProduct: snapshot SQL=%d, taxonomy full=%d, phrase lists=%d, values=%d B",
				reads,
				database.count(selectTaxonomy),
				database.count(listModerationPhrases),
				volume,
			)
			wantFull, wantPhrases := 0, 0
			if legacy {
				wantFull, wantPhrases = 2, 1
			}
			if database.count(selectTaxonomy) != wantFull || database.count(listModerationPhrases) != wantPhrases {
				t.Fatal("повторные загрузки на приёме", database.queries)
			}
			if err := indexer.SyncOffer(ctx, "t1", "p1"); err != nil {
				t.Fatal(err)
			}
			reads, volume = database.readVolume()
			t.Logf(
				"с индексацией: snapshot SQL=%d, taxonomy full=%d, phrase lists=%d, values=%d B",
				reads,
				database.count(selectTaxonomy),
				database.count(listModerationPhrases),
				volume,
			)
			if legacy {
				wantFull++
			}
			if database.count(selectTaxonomy) != wantFull {
				t.Fatal("повторные загрузки с индексацией", database.queries)
			}
			if !legacy && (reads != 4 || volume != 32) {
				t.Fatal("ожидались только четыре bigint версии/ревизии", reads, volume)
			}
			// Настоящая подготовка фасетов тоже читает общий снимок.
			queries := appcatalog.NewQueries(uow, nil, nil, nil, nil, fake.NewClock(time.Time{}))
			if _, err := queries.PrepareQuery(ctx, ports.OfferQuery{CategoryID: "c1"}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func BenchmarkApplyProductSnapshots(b *testing.B) {
	for _, legacy := range []bool{true, false} {
		b.Run(fmt.Sprintf("legacy=%v", legacy), func(b *testing.B) {
			database, projector, indexer, _ := snapshotProductPipeline(b, legacy)
			ctx := context.Background()
			b.ResetTimer()
			for iteration := 0; iteration < b.N; iteration++ {
				if err := projector.ApplyProduct(ctx, snapshotProductEvent(int64(iteration+1))); err != nil {
					b.Fatal(err)
				}
				if err := indexer.SyncOffer(ctx, "t1", "p1"); err != nil {
					b.Fatal(err)
				}
			}
			b.StopTimer()
			reads, volume := database.readVolume()
			b.ReportMetric(float64(reads)/float64(b.N), "snapshot-queries/event")
			b.ReportMetric(float64(volume)/float64(b.N), "snapshot-bytes/event")
			b.ReportMetric(float64(database.count(selectTaxonomy))/float64(b.N), "full-taxonomy/event")
		})
	}
}
