package fake_test

import (
	"context"
	"errors"
	"testing"

	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/cart"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
	"github.com/proshop-app/proshop-marketplace/pkg/money"
)

func TestOrderChecksCurrentDiscounts(t *testing.T) {
	for _, scenario := range []string{"empty", "changed", "matched"} {
		t.Run(scenario, func(t *testing.T) {
			platform := fake.NewPlatform()
			actual := []cart.Discount{{PromotionID: "auto", Amount: money.FromKopecks(10)}}
			platform.SetPromoPricing("t1", ports.GroupPricing{
				Discounts: map[ids.ProductID][]cart.Discount{"p1": actual},
			})
			item := ports.OrderItem{ProductID: "p1", Quantity: 1, Price: money.FromKopecks(100)}
			switch scenario {
			case "empty":
				item.Discounts = []cart.Discount{}
			case "changed":
				item.Discounts = []cart.Discount{{PromotionID: "auto", Amount: money.FromKopecks(20)}}
			case "matched":
				item.Discounts = actual
			}
			result, err := platform.CreateOrder(context.Background(), "t1", ports.CreateOrderRequest{
				ExternalCheckoutID: "checkout1",
				GroupID:            "t1",
				Items:              []ports.OrderItem{item},
			})
			if scenario == "empty" || scenario == "changed" {
				if !errors.Is(err, ports.ErrPriceChanged) {
					t.Fatal(result, err)
				}
			} else if err != nil || result.Total.Kopecks() != 90 {
				t.Fatal(result, err)
			}
		})
	}
}
