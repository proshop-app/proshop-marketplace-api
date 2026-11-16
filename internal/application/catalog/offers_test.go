package catalog_test

import (
	"context"
	"errors"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	appcatalog "github.com/proshop-app/proshop-marketplace/internal/application/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
	"github.com/proshop-app/proshop-marketplace/pkg/errs"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
	"github.com/proshop-app/proshop-marketplace/pkg/money"
)

func TestOffersOrderVisibilityAndPrices(t *testing.T) {
	store, index, platform := fake.NewStore(), fake.NewIndex(), fake.NewPlatform()
	for _, tenant := range []ids.TenantID{"t1", "t2"} {
		seedSeller(store, tenant, participant.TenantActive, true)
		platform.SetBuyerProfile("buyer", tenant, "personal")
	}
	requested := []ids.OfferID{
		{Tenant: "t2", Product: "p2"},
		{Tenant: "t1", Product: "hidden"},
		{Tenant: "t1", Product: "missing"},
		{Tenant: "t1", Product: "unlisted"},
		{Tenant: "t1", Product: "no-price"},
		{Tenant: "t1", Product: "p1"},
		{Tenant: "t2", Product: "p1"},
	}
	for _, id := range requested {
		if id.Product == "missing" {
			continue
		}
		doc := ports.OfferDocument{
			ID:        id.String(),
			TenantID:  string(id.Tenant),
			ProductID: string(id.Product),
			Visible:   id.Product != "hidden",
			Listed:    id.Product != "unlisted",
			PriceProfiles: []catalog.PriceProfile{{
				Key:            "personal",
				Price:          money.FromKopecks(12000),
				EffectivePrice: money.FromKopecks(9000),
			}},
		}
		if id.Product == "no-price" {
			doc.PriceProfiles = nil
		}
		if err := index.Put(context.Background(), []ports.OfferDocument{doc}); err != nil {
			t.Fatal(err)
		}
	}
	queries := appcatalog.NewQueries(store, index, platform, nil, nil, fake.NewClock(time.Time{}))
	cards, err := queries.Offers(context.Background(), requested, "buyer")
	if err != nil {
		t.Fatal(err)
	}
	got := make([]string, 0, len(cards))
	for _, card := range cards {
		got = append(got, card.Offer.ID)
		if card.Offer.Price != 9000 || card.Offer.BasePrice != 12000 {
			t.Fatal(card)
		}
	}
	if !reflect.DeepEqual(got, []string{"t2~p2", "t1~p1", "t2~p1"}) {
		t.Fatal(got)
	}
	calls := platform.PriceContextCalls()
	if len(calls) != 2 || calls[0] == calls[1] {
		t.Fatal("контекст должен вычисляться один раз на магазин", calls)
	}
}

func TestOffersIndexFailure(t *testing.T) {
	index := fake.NewIndex()
	failure := errors.New("индекс недоступен")
	index.BeforeGet = func(_ context.Context, id string) error {
		if id == "t1~p2" {
			return failure
		}
		return nil
	}
	queries := appcatalog.NewQueries(fake.NewStore(), index, fake.NewPlatform(), nil, nil, nil)
	requested := []ids.OfferID{
		{Tenant: "t1", Product: "p1"},
		{Tenant: "t1", Product: "p2"},
	}
	cards, err := queries.Offers(context.Background(), requested, "")
	if cards != nil || !errors.Is(err, failure) || errs.CodeOf(err) != errs.Unavailable {
		t.Fatal(cards, err)
	}
}

func TestOffersConcurrency(t *testing.T) {
	index := fake.NewIndex()
	var active atomic.Int32
	started := make(chan struct{}, 20)
	release := make(chan struct{})
	index.BeforeGet = func(ctx context.Context, _ string) error {
		if count := active.Add(1); count > 5 {
			t.Errorf("одновременно %d запросов", count)
		}
		defer active.Add(-1)
		started <- struct{}{}
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	queries := appcatalog.NewQueries(fake.NewStore(), index, fake.NewPlatform(), nil, nil, nil)
	requested := make([]ids.OfferID, 20)
	for i := range requested {
		requested[i] = ids.OfferID{Tenant: "t1", Product: "p1"}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := queries.Offers(ctx, requested, "")
		done <- err
	}()
	for range 5 {
		select {
		case <-started:
		case <-ctx.Done():
			t.Fatal("не запущены пять параллельных запросов")
		}
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
