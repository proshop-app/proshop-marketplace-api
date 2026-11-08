package catalog_test

import (
	"context"
	"errors"
	"testing"
	"time"

	appcatalog "github.com/proshop-app/proshop-marketplace/internal/application/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

func indexed(t *testing.T) (*fake.Store, *appcatalog.Queries) {
	t.Helper()

	store, index, clock := fake.NewStore(), fake.NewIndex(), fake.NewClock(t1)
	seedSeller(store, "t1", participant.TenantActive, true)
	seedSeller(store, "t2", participant.TenantActive, false)
	seedProduct(store, "t1", "p1")
	seedProduct(store, "t1", "p2")
	seedProduct(store, "t2", "p1")

	indexer := appcatalog.NewIndexer(store, index, clock, 10, &fake.IndexRebuildLock{}, nil, 30*time.Second)
	if _, err := indexer.Rebuild(context.Background()); err != nil {
		t.Fatal(err)
	}

	return store, appcatalog.NewQueries(store, index, fake.NewPlatform(), nil, nil, fake.NewClock(time.Time{}))
}

func TestSearchDefaultsAndFilters(t *testing.T) {
	_, queries := indexed(t)
	ctx := context.Background()

	list, err := queries.Search(ctx, ports.OfferQuery{})
	if err != nil || list.Total != 2 {
		t.Fatalf("в выдаче только предложения видимых участников: %+v, %v", list, err)
	}

	list, err = queries.Search(ctx, ports.OfferQuery{TenantIDs: []string{"t1"}, Limit: 1})
	if err != nil || list.Total != 2 || len(list.Items) != 1 {
		t.Fatalf("страница: %+v, %v", list, err)
	}
}

func TestSearchValidation(t *testing.T) {
	_, queries := indexed(t)
	ctx := context.Background()
	negative, low, high := int64(-1), int64(5), int64(1)
	long := make([]rune, 201)
	for i := range long {
		long[i] = 'а'
	}
	cases := map[string]ports.OfferQuery{
		"длинный запрос":        {Text: string(long)},
		"лимит больше 100":      {Limit: 101},
		"отрицательный":         {Limit: -1},
		"глубокая страница":     {Offset: 9990, Limit: 20},
		"смещение меньше 0":     {Offset: -1},
		"цена меньше 0":         {PriceMin: &negative},
		"границы наоборот":      {PriceMin: &low, PriceMax: &high},
		"порядок":               {Sort: "random"},
		"магазин":               {TenantIDs: []string{"bad id"}},
		"пустой магазин":        {TenantIDs: []string{""}},
		"много магазинов":       {TenantIDs: make([]string, appcatalog.MaxTenants+1)},
		"группа без магазина":   {GroupID: "g1"},
		"группа двух магазинов": {GroupID: "g1", TenantIDs: []string{"t1", "t2"}},
	}

	for name, query := range cases {
		if _, err := queries.Search(ctx, query); !errors.Is(err, appcatalog.ErrInvalidQuery) {
			t.Fatalf("%s: ожидалось ErrInvalidQuery, получено %v", name, err)
		}
	}
}

func TestOfferCard(t *testing.T) {
	_, queries := indexed(t)
	ctx := context.Background()

	card, err := queries.Offer(ctx, ids.OfferID{Tenant: "t1", Product: "p1"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if card.Offer.Name != "Товар p1" {
		t.Fatalf("карточка: %+v", card)
	}

	_, err = queries.Offer(ctx, ids.OfferID{Tenant: "t2", Product: "p1"}, "")
	if !errors.Is(err, catalog.ErrOfferNotFound) {
		t.Fatalf("скрытое предложение: ожидалось ErrOfferNotFound, получено %v", err)
	}

	_, err = queries.Offer(ctx, ids.OfferID{Tenant: "t9", Product: "p9"}, "")
	if !errors.Is(err, catalog.ErrOfferNotFound) {
		t.Fatalf("нет предложения: ожидалось ErrOfferNotFound, получено %v", err)
	}
}

func TestTenantFilterDeduplicates(t *testing.T) {
	_, queries := indexed(t)
	query, err := queries.PrepareQuery(context.Background(), ports.OfferQuery{
		TenantIDs: []string{"t1", "t1"},
		GroupID:   "g1",
	})
	if err != nil || len(query.TenantIDs) != 1 || query.TenantIDs[0] != "t1" {
		t.Fatalf("%+v %v", query, err)
	}
}

func TestSearchTrackQuery(t *testing.T) {
	cases := []struct {
		name  string
		query ports.OfferQuery
		want  bool
	}{
		{name: "отправка", query: ports.OfferQuery{Text: " Товар ", TrackQuery: true}, want: true},
		{name: "подсказка", query: ports.OfferQuery{Text: "Товар"}},
		{name: "пустой текст", query: ports.OfferQuery{TrackQuery: true}},
		{name: "пробелы", query: ports.OfferQuery{Text: " \t\n", TrackQuery: true}},
		{name: "следующая страница", query: ports.OfferQuery{Text: "Товар", TrackQuery: true, Offset: 20}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			index := &fake.PromotionIndex{}
			queries := appcatalog.NewQueries(
				fake.NewStore(),
				index,
				fake.NewPlatform(),
				nil,
				nil,
				fake.NewClock(time.Time{}),
			)
			if _, err := queries.Search(context.Background(), tc.query); err != nil {
				t.Fatal(err)
			}
			if index.Query.TrackQuery != tc.want {
				t.Fatalf("флаг поиска: %+v", index.Query)
			}
		})
	}
}
