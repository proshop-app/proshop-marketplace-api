package pricecontexts_test

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

func TestFreshPreloadOnlyFetchesMissingShopsAndKeepsOriginalAge(t *testing.T) {
	f := setup(t, config())
	loaded := f.clock.Now().Add(-30 * time.Second)
	seed(t, f, loaded)
	f.service.Preload(buyerID)
	wait(t, f, "ok")
	if calls := f.platform.PriceContextCalls(); len(calls) != 0 {
		t.Fatal("повтор входа перезапросил свежие профили", calls)
	}
	f.store.SeedTenant(participant.TenantState{TenantID: "t2", Status: participant.TenantActive})
	f.store.SeedParticipant(participant.State{
		TenantID: "t2",
		Enabled:  true,
		Stage:    participant.StageConnected,

		Delivery: participant.DeliverySettings{Regions: []string{"RU-MOW"}, Carriers: []string{"cdek"}},
	})
	f.service.Preload(buyerID)
	wait(t, f, "ok")
	if calls := f.platform.PriceContextCalls(); !reflect.DeepEqual(calls, []ids.TenantID{"t2"}) {
		t.Fatal("добавление магазина перезапросило остальные", calls)
	}
	entry, found, err := f.cache.Get(context.Background(), buyerID)
	if err != nil || !found || entry.Profiles["t1"] != "marketplace:buyer" || !entry.LoadedAt.Equal(loaded) {
		t.Fatal("пополнение продлило свежесть или потеряло личную цену", entry, err)
	}
	f.clock.Advance(30 * time.Second)
	f.service.Preload(buyerID)
	wait(t, f, "ok")
	want := []ids.TenantID{"t2", "t1", "t2"}
	if calls := f.platform.PriceContextCalls(); !reflect.DeepEqual(calls, want) {
		t.Fatal("истёкшие профили не перепроверены", calls)
	}
}
