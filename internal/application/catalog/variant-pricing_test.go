package catalog_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"

	appcatalog "github.com/proshop-app/proshop-marketplace/internal/application/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
	"github.com/proshop-app/proshop-marketplace/pkg/money"
)

type variantPricingPlatform struct {
	*fake.Platform
	contexts map[ids.TenantID]string
	calls    int
}

type variantPricingUOW struct {
	ports.UnitOfWork
	transactions  int
	batches       int
	singles       int
	batchSize     int
	batchErr      error
	singleErr     error
	taxonomyReads int
}

type variantPricingTx struct {
	ports.Tx
	uow *variantPricingUOW
}

type variantPricingOffers struct {
	ports.OfferStates
	uow *variantPricingUOW
}

func (u *variantPricingUOW) Do(ctx context.Context, fn func(context.Context, ports.Tx) error) error {
	u.transactions++
	return u.UnitOfWork.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		return fn(ctx, variantPricingTx{Tx: tx, uow: u})
	})
}

func (tx variantPricingTx) Offers() ports.OfferStates {
	return variantPricingOffers{OfferStates: tx.Tx.Offers(), uow: tx.uow}
}

func (tx variantPricingTx) Taxonomy() taxonomy.Repository {
	tx.uow.taxonomyReads++
	return tx.Tx.Taxonomy()
}

func (o variantPricingOffers) Batch(ctx context.Context, keys []ids.OfferID) ([]ports.OfferState, error) {
	o.uow.batches++
	o.uow.batchSize = len(keys)
	if o.uow.batchErr != nil {
		return nil, o.uow.batchErr
	}
	states, err := o.OfferStates.Batch(ctx, keys)
	// SQL не гарантирует порядок результатов пачки.
	slices.Reverse(states)
	return states, err
}

func (o variantPricingOffers) Offer(
	ctx context.Context,
	tenant ids.TenantID,
	product ids.ProductID,
) (ports.OfferState, error) {
	o.uow.singles++
	if o.uow.singleErr != nil {
		return ports.OfferState{}, o.uow.singleErr
	}
	return o.OfferStates.Offer(ctx, tenant, product)
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
		size    int
	}{
		{name: "гость", price: 9500, base: 10000},
		{name: "покупатель с контекстом", buyer: "buyer", profile: "personal", price: 7000, base: 8000},
		{name: "публичный контекст", buyer: "buyer", profile: catalog.DefaultPriceProfile, price: 9500, base: 10000},
		{name: "покупатель без контекста", buyer: "buyer", price: 9000, base: 12000},
		{name: "нет цены профиля", buyer: "buyer", profile: "missing"},
		{name: "скрытый магазин без контекста", buyer: "buyer", hidden: true},
		{name: "200 предложений без контекста", buyer: "buyer", price: 9000, base: 12000, size: 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			store, index := fake.NewStore(), fake.NewIndex()
			seedSeller(store, "t1", participant.TenantActive, true)
			size := max(2, tc.size)
			for i := range size {
				product := ids.ProductID(fmt.Sprintf("p%d", i+1))
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
			uow := &variantPricingUOW{UnitOfWork: store}
			queries := appcatalog.NewQueries(uow, index, platform, nil, nil, nil)
			reads := 0
			index.BeforeGet = func(context.Context, string) error {
				reads++
				return nil
			}
			group, err := queries.Variants(ctx, "t1", "group", tc.buyer)
			if err != nil || reads != 0 || platform.calls > 1 {
				t.Fatalf("ошибка, чтения карточек, запросы контекста: %v, %d, %d", err, reads, platform.calls)
			}
			wantTransactions, wantBatches := 0, 0
			if tc.buyer != "" {
				wantTransactions = 1 // Только существующий выбор контекста покупателя.
				if tc.profile == "" {
					wantTransactions++
					wantBatches = 1
				}
			}
			if uow.transactions != wantTransactions || uow.batches != wantBatches || uow.singles != 0 {
				t.Fatalf("транзакции, пачки, одиночные чтения: %d, %d, %d", uow.transactions, uow.batches, uow.singles)
			}
			if uow.taxonomyReads != 0 {
				t.Fatal("запрос вариантов читает таксономию")
			}
			if wantBatches > 0 && uow.batchSize != size {
				t.Fatalf("размер пачки: %d вместо %d", uow.batchSize, size)
			}
			wantItems := size
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
			wantSingles := 0
			if tc.buyer != "" && tc.profile == "" {
				wantSingles = 2
			}
			if uow.singles != wantSingles || uow.batches != wantBatches {
				t.Fatalf("карточки читают только своё публичное состояние на холодном пути: %+v", uow)
			}
		})
	}
}

func TestVariantsEmptyColdGroupSkipsBatch(t *testing.T) {
	store := fake.NewStore()
	seedSeller(store, "t1", participant.TenantActive, true)
	uow := &variantPricingUOW{UnitOfWork: store}
	platform := &variantPricingPlatform{Platform: fake.NewPlatform(), contexts: map[ids.TenantID]string{}}
	queries := appcatalog.NewQueries(uow, fake.NewIndex(), platform, nil, nil, nil)
	group, err := queries.Variants(context.Background(), "t1", "group", "buyer")
	if err != nil || len(group.Items) != 0 {
		t.Fatal(group, err)
	}
	if uow.transactions != 1 || uow.batches != 0 || uow.singles != 0 || uow.taxonomyReads != 0 {
		t.Fatalf("пустая группа не требует чтения состояний или таксономии: %+v", uow)
	}
}

func TestOfferColdPublicReadErrors(t *testing.T) {
	failure := errors.New("хранилище недоступно")
	for _, tc := range []struct {
		readErr error
		wantErr error
	}{
		{readErr: catalog.ErrProductNotFound, wantErr: catalog.ErrOfferNotFound},
		{readErr: failure, wantErr: failure},
	} {
		store, index := fake.NewStore(), fake.NewIndex()
		seedSeller(store, "t1", participant.TenantActive, true)
		ctx := context.Background()
		id := ids.OfferID{Tenant: "t1", Product: "p1"}
		if err := index.Put(ctx, []ports.OfferDocument{{ID: id.String(), Visible: true}}); err != nil {
			t.Fatal(err)
		}
		uow := &variantPricingUOW{UnitOfWork: store, singleErr: tc.readErr}
		platform := &variantPricingPlatform{Platform: fake.NewPlatform(), contexts: map[ids.TenantID]string{}}
		queries := appcatalog.NewQueries(uow, index, platform, nil, nil, nil)
		if _, err := queries.Offer(ctx, id, "buyer"); !errors.Is(err, tc.wantErr) {
			t.Fatalf("отказ карточки изменился: %v вместо %v", err, tc.wantErr)
		}
		if uow.transactions != 2 || uow.singles != 1 || uow.batches != 0 {
			t.Fatalf("карточка читает одно публичное состояние: %+v", uow)
		}
	}
}

func TestVariantsColdBatchMissingProductAndError(t *testing.T) {
	for _, failure := range []error{nil, errors.New("хранилище недоступно")} {
		store, index := fake.NewStore(), fake.NewIndex()
		seedSeller(store, "t1", participant.TenantActive, true)
		ctx := context.Background()
		id := ids.OfferID{Tenant: "t1", Product: "missing"}
		doc := ports.OfferDocument{
			ID:         id.String(),
			TenantID:   "t1",
			VariantsID: "group",
			Visible:    true,
		}
		if err := index.Put(ctx, []ports.OfferDocument{doc}); err != nil {
			t.Fatal(err)
		}
		uow := &variantPricingUOW{UnitOfWork: store, batchErr: failure}
		platform := &variantPricingPlatform{Platform: fake.NewPlatform(), contexts: map[ids.TenantID]string{}}
		queries := appcatalog.NewQueries(uow, index, platform, nil, nil, nil)
		group, err := queries.Variants(ctx, "t1", "group", "buyer")
		if uow.batches != 1 || uow.singles != 0 || uow.transactions != 2 {
			t.Fatalf("ожидалась одна пачка в транзакции группы: %+v", uow)
		}
		if failure != nil {
			if !errors.Is(err, failure) || group != nil {
				t.Fatal("ошибка чтения пачки потеряна", err)
			}
			continue
		}
		if err != nil || len(group.Items) != 0 {
			t.Fatal("отсутствующее предложение попало в группу", group, err)
		}
		if _, err := queries.Offer(ctx, id, "buyer"); !errors.Is(err, catalog.ErrOfferNotFound) {
			t.Fatal("отказ карточки изменился", err)
		}
	}
}
