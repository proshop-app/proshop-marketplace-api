package taxonomy_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	appcatalog "github.com/proshop-app/proshop-marketplace/internal/application/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	"github.com/proshop-app/proshop-marketplace/internal/application/moderationstatus"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	apptaxonomy "github.com/proshop-app/proshop-marketplace/internal/application/taxonomy"
	"github.com/proshop-app/proshop-marketplace/internal/domain/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/domain/moderation"
	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
	"github.com/proshop-app/proshop-marketplace/pkg/money"
)

type failPageOnceUOW struct {
	ports.UnitOfWork
	calls int
}

func (u *failPageOnceUOW) Do(ctx context.Context, fn func(context.Context, ports.Tx) error) error {
	u.calls++
	if u.calls == 2 {
		return context.DeadlineExceeded
	}
	return u.UnitOfWork.Do(ctx, fn)
}

func TestRevalidationResumesAndUsesLatestVersion(t *testing.T) {
	ctx := context.Background()
	store := fake.NewStore()
	definition := revalidationSchema()
	store.SeedTaxonomy(definition)
	clock := fake.NewClock(time.Now())
	store.SetDatabaseClock(clock)
	for i := 0; i < 120; i++ {
		seedRevalidationProduct(store, definition, ids.ProductID(fmt.Sprintf("p%03d", i)), "c1")
	}
	seedRevalidationProduct(store, definition, "unaffected", "c2")
	service := apptaxonomy.NewService(store, clock, nil)
	definition.Attributes[0].Name = "Изменённое поле"
	if _, err := service.LoadPart(ctx, "attributes", definition, 1); err != nil {
		t.Fatal(err)
	}
	if len(store.OutboxRows()) != 0 {
		t.Fatal("публикация обошла товары")
	}
	job, err := service.RevalidateNext(ctx)
	if err != nil || job == nil || job.Processed != 50 || job.Done {
		t.Fatal(job, err)
	}
	rows := len(store.OutboxRows())
	store.FailCommit = errors.New("сбой страницы")
	if job, err := service.RevalidateNext(ctx); err == nil || job != nil {
		t.Fatal("ошибка страницы не возвращена", job, err)
	}
	store.FailCommit = nil
	if len(store.OutboxRows()) != rows {
		t.Fatal("сбой страницы оставил задачи")
	}
	if state, _ := store.Product("t1", "p050"); state.Card.Publication.Version != 1 {
		t.Fatal("сбой страницы сохранил товар", state)
	}
	// Событие между страницами сразу использует новую голову, включая ключ позади курсора.
	state, _ := store.Product("t1", "p000")
	projector := appcatalog.NewProjector(store, clock, nil)
	if err := projector.ApplyProduct(ctx, appcatalog.ProductEvent{
		Snapshot: catalog.Snapshot{
			TenantID:  state.TenantID,
			ProductID: state.ProductID,
			Revision:  2,
			At:        clock.Now(),
			Card:      state.Card,
		},
	}); err != nil {
		t.Fatal(err)
	}
	if state, _ := store.Product("t1", "p000"); state.Revision != 2 || state.Card.Publication.Version != 2 {
		t.Fatal("событие не принято по новой версии", state)
	}
	definition.Attributes[0].Name = "Следующая версия"
	if _, err := service.LoadPart(ctx, "attributes", definition, 2); err != nil {
		t.Fatal(err)
	}
	// Новая версия закрывает старое задание этой категории и начинает новый проход с начала.
	service = apptaxonomy.NewService(store, clock, nil)
	job, err = service.RevalidateNext(ctx)
	if err != nil || job == nil || job.Version != 3 || job.Processed != 50 {
		t.Fatal("прогресс потерян", job, err)
	}
	if state, _ := store.Product("t1", "p049"); state.Card.Publication.Version != 3 {
		t.Fatal("старое задание вернуло старую версию", state)
	}
	drainRevalidation(t, service)
	for i := 0; i < 120; i++ {
		state, _ := store.Product("t1", ids.ProductID(fmt.Sprintf("p%03d", i)))
		if state.Card.Publication.Version != 3 {
			t.Fatal("страница потеряла товар", state)
		}
	}
	if state, _ := store.Product("t1", "unaffected"); state.Version != 1 || state.Card.Publication.Version != 1 {
		t.Fatal("затронута посторонняя схема", state)
	}
	rows = len(store.OutboxRows())
	drainRevalidation(t, service)
	if len(store.OutboxRows()) != rows {
		t.Fatal("завершённая перепроверка повторилась")
	}
}

func revalidationSchema() taxonomy.Definition {
	d := schema(1)
	d.Attributes = []taxonomy.Attribute{
		{Code: "a1", Name: "Поле", Type: "string"},
		{Code: "a2", Name: "Другое поле", Type: "string"},
	}
	d.Schemas["s2"] = taxonomy.Schema{Attributes: []string{"a2"}, Optional: []string{"a2"}}
	d.Categories = append(d.Categories, taxonomy.Category{ID: "c2", Slug: "c2", Name: "Другая", Schema: "s2"})
	return d
}

func seedRevalidationProduct(store *fake.Store, d taxonomy.Definition, id ids.ProductID, category string) {
	card := &taxonomy.Card{CategoryID: category, Published: true}
	store.SeedProduct(catalog.ProductState{
		TenantID:  "t1",
		ProductID: id,
		Revision:  1,
		Version:   1,
		Card: catalog.Card{
			Name:        "Товар",
			Visible:     true,
			Marketplace: card,
			Publication: d.Check(card),
		},
	})
}

func TestTransientPageDefersOnlyItsCategory(t *testing.T) {
	store := fake.NewStore()
	definition := revalidationSchema()
	store.SeedTaxonomy(definition)
	clock := fake.NewClock(time.Now())
	store.SetDatabaseClock(clock)
	seedRevalidationProduct(store, definition, "p1", "c1")
	seedRevalidationProduct(store, definition, "p2", "c2")
	service := apptaxonomy.NewService(store, clock, nil)
	definition.Attributes[0].Name = "Новое поле 1"
	definition.Attributes[1].Name = "Новое поле 2"
	if _, err := service.LoadPart(context.Background(), "attributes", definition, 1); err != nil {
		t.Fatal(err)
	}
	failing := &failPageOnceUOW{UnitOfWork: store}
	service = apptaxonomy.NewService(failing, clock, nil)
	if _, err := service.RevalidateNext(context.Background()); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	first, _ := store.Product("t1", "p1")
	if first.Card.Publication.Version != 1 {
		t.Fatal("неудачная страница сохранила товар")
	}
	job, err := service.RevalidateNext(context.Background())
	if err != nil || job == nil || job.Category != "c2" {
		t.Fatal("временная ошибка остановила чужую категорию", job, err)
	}
	clock.Advance(time.Second)
	job, err = service.RevalidateNext(context.Background())
	if err != nil || job == nil || job.Category != "c1" || !job.Done {
		t.Fatal("отложенная страница не возобновилась", job, err)
	}
}

func TestRevalidationLeaseUsesDatabaseClock(t *testing.T) {
	ctx := context.Background()
	store := fake.NewStore()
	databaseClock := fake.NewClock(time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC))
	store.SetDatabaseClock(databaseClock)
	applicationClock := fake.NewClock(databaseClock.Now().Add(24 * time.Hour))
	definition := revalidationSchema()
	store.SeedTaxonomy(definition)
	seedRevalidationProduct(store, definition, "p1", "c1")
	service := apptaxonomy.NewService(store, applicationClock, nil)
	definition.Attributes[0].Name = "Новое имя"
	if _, err := service.LoadPart(ctx, "attributes", definition, 1); err != nil {
		t.Fatal(err)
	}
	failing := apptaxonomy.NewService(&failPageOnceUOW{UnitOfWork: store}, applicationClock, nil)
	if _, err := failing.RevalidateNext(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	applicationClock.Advance(24 * time.Hour)
	if job, err := service.RevalidateNext(ctx); err != nil || job != nil {
		t.Fatal("часы приложения досрочно завершили аренду БД", job, err)
	}
	databaseClock.Advance(time.Second)
	if job, err := service.RevalidateNext(ctx); err != nil || job == nil || !job.Done {
		t.Fatal("часы БД не освободили задание", job, err)
	}
}

func TestSupersedingKeepsOtherCategoryJobs(t *testing.T) {
	store := fake.NewStore()
	definition := revalidationSchema()
	store.SeedTaxonomy(definition)
	clock := fake.NewClock(time.Now())
	store.SetDatabaseClock(clock)
	seedRevalidationProduct(store, definition, "p1", "c1")
	seedRevalidationProduct(store, definition, "p2", "c2")
	service := apptaxonomy.NewService(store, clock, nil)
	definition.Attributes[0].Name = "Новое поле 1"
	definition.Attributes[1].Name = "Новое поле 2"
	if _, err := service.LoadPart(context.Background(), "attributes", definition, 1); err != nil {
		t.Fatal(err)
	}
	definition.Attributes[0].Name = "Ещё новое поле 1"
	if _, err := service.LoadPart(context.Background(), "attributes", definition, 2); err != nil {
		t.Fatal(err)
	}
	job, err := service.RevalidateNext(context.Background())
	if err != nil || job == nil || job.Version != 2 || job.Category != "c2" {
		t.Fatal("новый выпуск закрыл постороннюю категорию", job, err)
	}
	job, err = service.RevalidateNext(context.Background())
	if err != nil || job == nil || job.Version != 3 || job.Category != "c1" {
		t.Fatal("осталось устаревшее задание", job, err)
	}
}

func TestUnchangedPublicationVersionRemainsValidForReaders(t *testing.T) {
	ctx := context.Background()
	store := fake.NewStore()
	definition := revalidationSchema()
	store.SeedTaxonomy(definition)
	clock := fake.NewClock(time.Now())
	store.SetDatabaseClock(clock)
	seedRevalidationProduct(store, definition, "p2", "c2")
	service := apptaxonomy.NewService(store, clock, nil)
	definition.Attributes[0].Name = "Изменено только для c1"
	if _, err := service.LoadPart(ctx, "attributes", definition, 1); err != nil {
		t.Fatal(err)
	}
	drainRevalidation(t, service)
	index := fake.NewIndex()
	if err := index.Put(ctx, []ports.OfferDocument{
		{
			ID:           "t1~p2",
			TenantID:     "t1",
			ProductID:    "p2",
			Visible:      true,
			Listed:       true,
			Name:         "Товар",
			CategoryID:   "c2",
			CategoryPath: []string{"c2"},
			Price:        money.FromKopecks(100).Kopecks(),
			BasePrice:    money.FromKopecks(100).Kopecks(),
		},
	}); err != nil {
		t.Fatal(err)
	}
	queries := appcatalog.NewQueries(store, index, nil, nil, nil, clock)
	diagnostic, err := queries.Publication(ctx, ids.OfferID{Tenant: "t1", Product: "p2"})
	if err != nil || diagnostic.Version != 1 || !diagnostic.Published {
		t.Fatal("диагностика посчитала старую версию невалидной", diagnostic, err)
	}
	page, err := queries.Search(ctx, ports.OfferQuery{CategoryID: "c2"})
	if err != nil || page.Total != 1 {
		t.Fatal("поиск скрыл незатронутую карточку", page, err)
	}
	verdict := moderation.RestoreVerdict(moderation.VerdictState{
		Status:          moderation.Allowed,
		Revision:        1,
		TaxonomyVersion: 1,
	})
	status, _ := moderationstatus.Status(diagnostic.Publication, verdict)
	if status != "published" {
		t.Fatal("статус скрыл незатронутую карточку", status)
	}
	failures, err := service.RevalidationFailures(ctx, ports.OfferKey{}, 100)
	if err != nil || len(failures) != 0 {
		t.Fatal("отставшая версия сама создала ошибку", failures, err)
	}
}
