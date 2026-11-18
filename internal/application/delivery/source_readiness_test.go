package delivery_test

import (
	"context"
	"errors"
	"testing"

	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

func TestFallbackSourceIsSelectedAfterPreviousCallAndSkipsDisconnectedShop(t *testing.T) {
	service, platform, store := setup(t, 3)
	store.SeedTenant(participant.TenantState{TenantID: "t3", Status: participant.TenantActive})
	state, _ := store.Participant("t2")
	state.TenantID = "t3"
	store.SeedParticipant(state)
	platform.SetPickupPoints("t3", answer())
	platform.BeforePickup = func(_ context.Context, tenant ids.TenantID) error {
		if tenant == "t1" {
			state, _ := store.Participant("t2")
			state.Delivery = participant.DeliverySettings{}
			store.SeedParticipant(state)
			return errors.New("предыдущий источник не ответил; за это время следующему отключили службу")
		}
		return nil
	}
	points, err := service.Search(context.Background(), "cdek", "Город")
	if err != nil || len(points) != 1 {
		t.Fatal(points, err)
	}
	calls := platform.PickupCalls()
	if len(calls) != 2 || calls[0].Tenant != "t1" || calls[1].Tenant != "t3" {
		t.Fatal("пачка источников не должна переживать отключение доставки у магазина", calls)
	}
}
