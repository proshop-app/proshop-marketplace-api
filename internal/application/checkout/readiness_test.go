package checkout_test

import (
	"context"
	"errors"
	"testing"

	"github.com/proshop-app/proshop-marketplace/internal/domain/checkout"
	"github.com/proshop-app/proshop-marketplace/internal/domain/moderation"
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
)

func TestReadinessBeforeOrdersAndPayment(t *testing.T) {
	for _, phase := range []string{"orders", "worker", "payment", "payment_confirm"} {
		for _, condition := range []string{
			"participant_missing",
			"registry_missing",
			"disabled",
			"inactive",
			"carrier",
			"carrier_empty",
			"region",
			"region_empty",
			"product_missing",
			"product_deleted",
			"price_profile",
			"card_hidden",
			"unpublished",
			"verdict_missing",
			"rejected",
		} {
			t.Run(phase+"/"+condition, func(t *testing.T) {
				e := setup(t)
				ctx := context.Background()
				state := confirmedPurchase(t, e)
				if phase == "worker" {
					if _, err := e.service.CreateOrders(ctx, buyerID, state.ID, orderContact); err != nil {
						t.Fatal(err)
					}
				} else if phase != "orders" {
					var err error
					state, err = beginAndExecute(ctx, e, buyerID, state.ID, orderContact)
					if err != nil {
						t.Fatal(err)
					}
				}
				p, _ := e.store.Participant("t1")
				product, _ := e.store.Product("t1", "p1")
				switch condition {
				case "participant_missing":
					e.store.RemoveParticipant("t1")
				case "registry_missing":
					e.store.RemoveTenant("t1")
				case "disabled":
					p.Stage = participant.StageDisconnected
				case "inactive":
					e.store.SeedTenant(participant.TenantState{TenantID: "t1", Status: participant.TenantSuspended})
				case "carrier":
					p.Delivery.Carriers = []string{"unknown"}
				case "carrier_empty":
					p.Delivery.Carriers = nil
				case "product_missing":
					e.store.RemoveProduct("t1", "p1")
				case "product_deleted":
					product.Deleted = true
				case "price_profile":
					product.PriceProfiles = nil
				case "card_hidden":
					product.Card.Visible = false
				case "unpublished":
					product.Card.Publication.Published = false
				case "verdict_missing":
					e.store.RemoveVerdict("t1", "p1")
				case "rejected":
					e.store.SeedVerdict(moderation.VerdictState{
						TenantID:  "t1",
						ProductID: "p1",
						Status:    moderation.Rejected,
					})
				case "region":
					p.Delivery.Regions = []string{"unknown"}
				case "region_empty":
					p.Delivery.Regions = nil
				}
				if condition != "participant_missing" {
					e.store.SeedParticipant(p)
				}
				if condition != "product_missing" {
					e.store.SeedProduct(product)
				}
				var err error
				switch phase {
				case "worker":
					if err := executeTasks(ctx, e); err != nil {
						t.Fatal(err)
					}
					current, err := e.service.Get(ctx, buyerID, state.ID)
					if err != nil || current.Groups[0].Status != checkout.GroupRejected ||
						!current.Groups[0].ReconciliationRequired || current.Status != checkout.StatusDraft ||
						e.platform.OrderCount() != 0 {
						t.Fatal(current.Groups, err, e.platform.OrderCount())
					}
					return
				case "payment_confirm":
					err = e.service.ConfirmPayment(ctx, buyerID, state.ID)
				case "payment":
					_, err = e.service.Pay(ctx, buyerID, state.ID)
				default:
					_, err = e.service.CreateOrders(ctx, buyerID, state.ID, orderContact)
				}
				if !errors.Is(err, checkout.ErrDeliveryChanged) {
					t.Fatal(err)
				}
			},
			)
		}
	}
}
