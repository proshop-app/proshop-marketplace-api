package catalog_test

import (
	"context"
	"errors"
	"testing"

	appcatalog "github.com/proshop-app/proshop-marketplace/internal/application/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
	"github.com/proshop-app/proshop-marketplace/pkg/money"
)

type variantPricingPlatform struct {
	*fake.Platform
	contexts map[ids.TenantID]string
	calls    int
}

func (p *variantPricingPlatform) PriceContexts(
	_ context.Context,
	_ ids.UUID,
	_ []ids.TenantID,
) (map[ids.TenantID]string, error) {
	p.calls++
	return p.contexts, nil
}

func TestVariantsUseSameBuyerPricesAsOffer(t *testing.T) {
	for _, tc := range []struct {
		name    string
		buyer   ids.UUID
		profile string
		price   int64
		base    int64
		hidden  bool
	}{
		{name: "гость", price: 9500, base: 10000},
		{name: "покупатель с контекстом", buyer: "buyer", profile: "personal", price: 7000, base: 8000},
		{name: "публичный контекст", buyer: "buyer", profile: catalog.DefaultPriceProfile, price: 9500, base: 10000},
		{name: "покупатель без контекста", buyer: "buyer", price: 9000, base: 12000},
		{name: "нет цены профиля", buyer: "buyer", profile: "missing"},
		{name: "скрытый магазин без контекста", buyer: "buyer", hidden: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			store, index := fake.NewStore(), fake.NewIndex()
			seedSeller(store, "t1", participant.TenantActive, true)
			for i, product := range []ids.ProductID{"p1", "p2"} {
				seedProduct(store, "t1", product)
				state, _ := store.Product("t1", product)
				state.Card.VariantsID = "group"
				state.Card.Variants = &catalog.Variants{
					GroupID:  "group",
					Position: i,
					Axes: []catalog.VariantAxis{{
						Key:   "platform.size",
						Code:  "size",
						Name:  "Размер",
						Value: string(product),
					}},
				}
				state.PriceProfiles = []catalog.PriceProfile{{
					Key:            catalog.DefaultPriceProfile,
					Price:          money.FromKopecks(12000),
					EffectivePrice: money.FromKopecks(9000),
				}}
				store.SeedProduct(state)
				err := store.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
					state, err := tx.Offers().Offer(ctx, "t1", product)
					if err != nil {
						return err
					}
					doc, _ := appcatalog.Document(state)
					// Индекс отстаёт от публичной проекции: холодный путь обязан перечитать её для каждого товара.
					doc.PriceProfiles = []catalog.PriceProfile{
						{
							Key:            catalog.DefaultPriceProfile,
							Price:          money.FromKopecks(10000),
							EffectivePrice: money.FromKopecks(9500),
						},
						{
							Key:            "personal",
							Price:          money.FromKopecks(8000),
							EffectivePrice: money.FromKopecks(7000),
						},
					}
					return index.Put(ctx, []ports.OfferDocument{doc})
				})
				if err != nil {
					t.Fatal(err)
				}
			}
			if tc.hidden {
				seedSeller(store, "t1", participant.TenantSuspended, true)
			}
			platform := &variantPricingPlatform{
				Platform: fake.NewPlatform(),
				contexts: map[ids.TenantID]string{},
			}
			if tc.profile != "" {
				platform.contexts["t1"] = tc.profile
			}
			queries := appcatalog.NewQueries(store, index, platform, nil, nil, nil)
			reads := 0
			index.BeforeGet = func(context.Context, string) error {
				reads++
				return nil
			}
			group, err := queries.Variants(ctx, "t1", "group", tc.buyer)
			if err != nil || reads != 0 || platform.calls > 1 {
				t.Fatalf("ошибка, чтения карточек, запросы контекста: %v, %d, %d", err, reads, platform.calls)
			}
			wantItems := 2
			if tc.price == 0 {
				wantItems = 0
			}
			if len(group.Items) != wantItems {
				t.Fatalf("состав группы: %+v", group)
			}
			for _, product := range []ids.ProductID{"p1", "p2"} {
				card, err := queries.Offer(ctx, ids.OfferID{Tenant: "t1", Product: product}, tc.buyer)
				if tc.price == 0 {
					if !errors.Is(err, catalog.ErrOfferNotFound) {
						t.Fatal("недоступная карточка", err)
					}
					continue
				}
				if err != nil || card.Offer.Price != tc.price || card.Offer.BasePrice != tc.base {
					t.Fatalf("цена карточки: %+v %v", card, err)
				}
				for _, item := range group.Items {
					samePrice := item.Price == card.Offer.Price && item.BasePrice == card.Offer.BasePrice
					if item.ID == card.Offer.ID && !samePrice {
						t.Fatalf("цена варианта расходится с карточкой: %+v %+v", item, card)
					}
				}
			}
		})
	}
}
