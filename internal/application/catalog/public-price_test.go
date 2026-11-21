package catalog_test

import (
	"context"
	"fmt"
	"testing"

	app "github.com/proshop-app/proshop-marketplace/internal/application/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

type publicPriceStore struct {
	ports.UnitOfWork
	calls int
}

type publicPriceTx struct {
	ports.Tx
}

func (s *publicPriceStore) Do(ctx context.Context, run func(context.Context, ports.Tx) error) error {
	s.calls++
	return s.UnitOfWork.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		return run(ctx, publicPriceTx{Tx: tx})
	})
}

func (publicPriceTx) Participants() participant.Repository {
	panic("поиск не должен читать магазины для выбора цены")
}

func TestSearchPriceDoesNotEnumerateTenants(t *testing.T) {
	for _, size := range []int{1, 1000} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			store, index := fake.NewStore(), fake.NewIndex()
			for i := 0; i < size; i++ {
				seedSeller(store, ids.TenantID(fmt.Sprintf("t%d", i)), participant.TenantActive, true)
			}
			if err := index.Put(context.Background(), []ports.OfferDocument{
				{
					ID:        "t0~p1",
					TenantID:  "t0",
					ProductID: "p1",
					Visible:   true,
					Listed:    true,
					Price:     0,
					BasePrice: 100,
				},
			}); err != nil {
				t.Fatal(err)
			}
			uow := &publicPriceStore{UnitOfWork: store}
			queries := app.NewQueries(uow, index, nil, nil, nil, nil)
			result, err := queries.Search(context.Background(), ports.OfferQuery{})
			if err != nil || uow.calls != 1 || result.Total != 1 || result.Items[0].Offer.Price != 0 {
				t.Fatal(result, err, uow.calls)
			}
		})
	}
}

func TestCardAndVariantsUseIndexedPublicPriceWithoutDatabase(t *testing.T) {
	ctx := context.Background()
	index := fake.NewIndex()
	doc := ports.OfferDocument{
		ID:         "t1~p1",
		TenantID:   "t1",
		ProductID:  "p1",
		Visible:    true,
		Listed:     true,
		Price:      0,
		BasePrice:  100,
		VariantsID: "g1",
		Variants: &catalog.Variants{
			GroupID: "g1",
			Axes:    []catalog.VariantAxis{{Key: "a1", Value: "v1"}},
		},
	}
	if err := index.Put(ctx, []ports.OfferDocument{doc}); err != nil {
		t.Fatal(err)
	}
	queries := app.NewQueries(nil, index, nil, nil, nil, nil)
	card, err := queries.Offer(ctx, ids.OfferID{Tenant: "t1", Product: "p1"})
	if err != nil || card.Offer.Price != 0 || card.Offer.BasePrice != 100 {
		t.Fatal(card, err)
	}
	variants, err := queries.Variants(ctx, "t1", "g1")
	if err != nil || len(variants.Items) != 1 || variants.Items[0].Price != 0 || variants.Items[0].BasePrice != 100 {
		t.Fatal(variants, err)
	}
}
