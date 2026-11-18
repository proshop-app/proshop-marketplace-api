package http

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	appdelivery "github.com/proshop-app/proshop-marketplace/internal/application/delivery"
	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/checkout"
	"github.com/proshop-app/proshop-marketplace/internal/domain/delivery"
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

func TestCheckoutPickupRegionRefusalIsExplicitHTTPError(t *testing.T) {
	const checkoutID ids.UUID = "00000000-0000-4000-8000-000000000001"
	const buyerID ids.UUID = "00000000-0000-4000-8000-000000000002"
	store := fake.NewStore()
	store.SeedTenant(participant.TenantState{TenantID: "t1", Status: participant.TenantActive})
	store.SeedParticipant(participant.State{
		TenantID: "t1",
		Stage:    participant.StageConnected,
		Enabled:  true,
		Delivery: participant.DeliverySettings{
			Regions:  []string{"RU-TA"},
			Carriers: []string{"cdek"},
		},
	})
	purchase := checkout.Restore(checkout.State{
		ID:      checkoutID,
		BuyerID: buyerID,
		Groups: []checkout.Group{{
			TenantID: "t1",
			Regions:  []string{"RU-TA"},
			Carriers: []string{"cdek"},
		}},
	})
	if err := store.Do(context.Background(), func(ctx context.Context, tx ports.Tx) error {
		return tx.Checkouts().Add(ctx, purchase)
	}); err != nil {
		t.Fatal(err)
	}
	platform := fake.NewPlatform()
	platform.SetPickupPoints("t1", ports.PickupPoints{
		Available: true,
		Items: []delivery.PickupPoint{{
			Carrier:    "cdek",
			Code:       "MOS4",
			City:       "Москва",
			RegionCode: "RU-MOW",
		}},
	})
	directory := appdelivery.NewService(store, platform, fake.NewPickupCache(), appdelivery.Config{
		Attempts:     1,
		TTL:          time.Hour,
		QuoteTimeout: time.Second,
	})
	handler := deliveryHandlers{directory: directory}
	request := httptest.NewRequest(http.MethodGet, "/?carrier=cdek&city=Москва&tenantId=t1", nil)
	request.SetPathValue("checkoutId", string(checkoutID))
	response := httptest.NewRecorder()
	handler.checkoutPickupPoints(response, withBuyer(request, buyerID))
	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusBadRequest || body.Error.Code != "delivery.region_not_allowed" {
		t.Fatal(response.Code, response.Body.String())
	}
}
