package catalog_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	appcatalog "github.com/proshop-app/proshop-marketplace/internal/application/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/domain/moderation"
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

func sellersState(t *testing.T, tenant ids.TenantID, product ids.ProductID) ports.OfferState {
	t.Helper()
	store := fake.NewStore()
	seedSeller(store, tenant, participant.TenantActive, true)
	seedProduct(store, tenant, product)
	var state ports.OfferState
	err := store.Do(context.Background(), func(ctx context.Context, tx ports.Tx) error {
		var err error
		state, err = tx.Offers().Offer(ctx, tenant, product)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	state.Product.SetModelKey("model", "identity")
	return state
}

func TestPopulateSellersVisibility(t *testing.T) {
	cases := []string{"visible", "participant", "tenant", "delivery", "product", "publication", "verdict", "missing"}
	for _, name := range cases {
		t.Run(name, func(t *testing.T) {
			first := sellersState(t, "t1", "p1")
			second := sellersState(t, "t2", "p1")
			member, product := second.Participant.State(), second.Product.State()
			switch name {
			case "participant":
				member.Stage = participant.StageCandidate
			case "tenant":
				second.Tenant = nil
			case "delivery":
				member.Delivery.Regions = nil
			case "product":
				product.Card.Visible = false
			case "publication":
				product.Card.Publication.Published = false
			case "verdict":
				second.Verdict = moderation.NewVerdict("t2", "p1")
			case "missing":
				second.Verdict = nil
			}
			second.Participant = participant.Restore(member)
			second.Product = catalog.RestoreProduct(product)
			counter := &fake.SellersCounts{States: []ports.OfferState{first, second, sellersState(t, "t1", "p2")}}
			cards := []appcatalog.OfferCard{
				{Offer: ports.OfferDocument{ModelKey: "model", TenantID: "t1"}},
				{Offer: ports.OfferDocument{ModelKey: "model", TenantID: "t2"}},
				{Offer: ports.OfferDocument{TenantID: "t3"}},
			}
			if err := appcatalog.PopulateSellers(context.Background(), counter, cards); err != nil {
				t.Fatal(err)
			}
			want := 1
			if name == "visible" {
				want = 2
			}
			for _, card := range cards[:2] {
				if card.SellersCount == nil || *card.SellersCount != want {
					t.Fatalf("счётчик: %+v, ожидалось %d", card.SellersCount, want)
				}
			}
			if cards[2].SellersCount != nil || counter.Calls != 1 ||
				!reflect.DeepEqual(counter.Keys, []string{"model"}) {
				t.Fatal(cards, counter)
			}
		})
	}
}

func TestPopulateSellersFailureAndEmpty(t *testing.T) {
	counter := &fake.SellersCounts{Err: errors.New("отказ")}
	cards := []appcatalog.OfferCard{{Offer: ports.OfferDocument{ModelKey: "model"}}}
	if err := appcatalog.PopulateSellers(context.Background(), counter, cards); !errors.Is(err, counter.Err) {
		t.Fatal(err)
	}
	if cards[0].SellersCount != nil {
		t.Fatal(cards)
	}
	for _, empty := range [][]appcatalog.OfferCard{nil, {{}}} {
		if err := appcatalog.PopulateSellers(context.Background(), counter, empty); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := appcatalog.PopulateSellers(ctx, counter, cards); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if counter.Calls != 1 {
		t.Fatal(counter.Calls)
	}
}

func TestPopulateSellersIndexMismatch(t *testing.T) {
	for name, counts := range map[string]map[string]int{
		"zero":     {"model": 0},
		"missing":  {},
		"negative": {"model": -1},
	} {
		t.Run(name, func(t *testing.T) {
			previous := 2
			cards := []appcatalog.OfferCard{{
				Offer:        ports.OfferDocument{ModelKey: "model"},
				SellersCount: &previous,
			}}
			counter := &fake.SellersCounts{Counts: counts}
			if err := appcatalog.PopulateSellers(context.Background(), counter, cards); err != nil {
				t.Fatal(err)
			}
			if cards[0].SellersCount != nil || counter.Calls != 1 ||
				!reflect.DeepEqual(counter.Keys, []string{"model"}) {
				t.Fatal(cards, counter)
			}
		})
	}
}
