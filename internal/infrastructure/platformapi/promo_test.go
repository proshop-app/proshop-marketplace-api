package platformapi_test

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/cart"
	"github.com/proshop-app/proshop-marketplace/internal/domain/checkout"
	"github.com/proshop-app/proshop-marketplace/pkg/errs"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
	"github.com/proshop-app/proshop-marketplace/pkg/money"
)

func TestPromoContextContract(t *testing.T) {
	for _, valid := range []bool{false, true} {
		name := map[bool]string{true: "promo_context_valid.json", false: "promo_context_invalid.json"}[valid]
		t.Run(name, func(t *testing.T) {
			client := server(
				t,
				map[string][2]string{
					"POST /api/v1/internal/marketplace/pricing/contexts": {"promo_context_request.json", name},
				},
				http.StatusOK,
			)
			result, err := client.PriceGroup(
				context.Background(),
				"t1",
				ports.GroupPricingRequest{
					BuyerID:   ids.UUID("7a1c2b1e-3c4d-4e5f-8a9b-0c1d2e3f4a5b"),
					PromoCode: "SAVE",
					Items:     []ports.ItemRequest{{ProductID: "p1", Quantity: 3}},
				},
			)
			if err != nil || result.PromoCode == nil || result.PromoCode.Valid != valid {
				t.Fatal(result, err)
			}
			amount, err := cart.DiscountedAmount(result.Items[0].Price, 3, result.Discounts["p1"])
			want := int64(300)
			if valid {
				want = 289
			}
			if err != nil || amount.Kopecks() != want {
				t.Fatal(amount, err)
			}
		})
	}
}

func TestPromoOrderContract(t *testing.T) {
	for _, valid := range []bool{false, true} {
		name := map[bool]string{true: "promo_order_valid.json", false: "promo_order_invalid.json"}[valid]
		t.Run(name, func(t *testing.T) {
			client := server(
				t,
				map[string][2]string{
					"POST " + prefix + "/orders": {"promo_order_request.json", name},
				},
				http.StatusOK,
			)
			request := orderRequest()
			request.PromoCode = "SAVE"
			request.Items[0].Discounts = []cart.Discount{{PromotionID: "promo1", Amount: money.FromKopecks(11)}}
			_, err := client.CreateOrder(context.Background(), "t1", request)
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestOrderTrackingContract(t *testing.T) {
	client := server(
		t,
		map[string][2]string{
			"GET " + prefix + "/orders/o1": {"", "order_tracking.json"},
		},
		http.StatusOK,
	)
	order, err := client.GetOrder(context.Background(), "t1", "o1")
	if err != nil || order.Tracking == nil || *order.Tracking.Number != "123" || order.Tracking.Carrier != "carrier" {
		t.Fatal(order, err)
	}
}

func TestPromoInvalidResponses(t *testing.T) {
	for _, name := range []string{"null_code", "null_message", "missing_item", "foreign_tenant", "excess_discount"} {
		t.Run(name, func(t *testing.T) {
			client := server(
				t,
				map[string][2]string{
					"POST /api/v1/internal/marketplace/pricing/contexts": {
						"promo_context_request.json",
						"promo_context_" + name + ".json",
					},
				},
				http.StatusOK,
			)
			_, err := client.PriceGroup(context.Background(), "t1", ports.GroupPricingRequest{
				BuyerID:   ids.UUID("7a1c2b1e-3c4d-4e5f-8a9b-0c1d2e3f4a5b"),
				PromoCode: "SAVE",
				Items:     []ports.ItemRequest{{ProductID: "p1", Quantity: 3}},
			})
			if err == nil {
				t.Fatal("некорректный ответ принят")
			}
		})
	}
}

func TestPromoOrderRefusal(t *testing.T) {
	client := server(
		t,
		map[string][2]string{
			"POST " + prefix + "/orders": {"promo_order_request.json", "order_promo_refused.json"},
		},
		http.StatusConflict,
	)
	request := orderRequest()
	request.PromoCode = "SAVE"
	request.Items[0].Discounts = []cart.Discount{{PromotionID: "promo1", Amount: money.FromKopecks(11)}}
	_, err := client.CreateOrder(context.Background(), "t1", request)
	typed, ok := errs.As(err)
	if !errors.Is(err, checkout.ErrPromoInvalid) || !ok || typed.Message() != "промокод истёк" {
		t.Fatal(err)
	}
}

func TestTrackingInvalidOrAbsent(t *testing.T) {
	for _, name := range []string{"null", "invalid_url", "empty_carrier"} {
		t.Run(name, func(t *testing.T) {
			client := server(
				t,
				map[string][2]string{
					"GET " + prefix + "/orders/o1": {"", "order_tracking_" + name + ".json"},
				},
				http.StatusOK,
			)
			order, err := client.GetOrder(context.Background(), "t1", "o1")
			if err != nil || order.Tracking != nil {
				t.Fatal(order, err)
			}
		})
	}
}

func TestPricingWithoutCode(t *testing.T) {
	client := server(
		t,
		map[string][2]string{
			"POST /api/v1/internal/marketplace/pricing/contexts": {
				"promo_context_no_code_request.json",
				"promo_context_no_code.json",
			},
		},
		http.StatusOK,
	)
	result, err := client.PriceGroup(context.Background(), "t1", ports.GroupPricingRequest{
		BuyerID: ids.UUID("7a1c2b1e-3c4d-4e5f-8a9b-0c1d2e3f4a5b"),
		Items:   []ports.ItemRequest{{ProductID: "p1", Quantity: 3}},
	})
	if err != nil || result.PromoCode != nil || len(result.Discounts["p1"]) != 1 {
		t.Fatal(result, err)
	}
}

func TestTrackingWithoutNumber(t *testing.T) {
	client := server(
		t,
		map[string][2]string{
			"GET " + prefix + "/orders/o1": {"", "order_tracking_null_number.json"},
		},
		http.StatusOK,
	)
	order, err := client.GetOrder(context.Background(), "t1", "o1")
	if err != nil || order.Tracking == nil || order.Tracking.Number != nil {
		t.Fatal(order, err)
	}
}

func TestTrackingEmptyNumber(t *testing.T) {
	client := server(
		t,
		map[string][2]string{
			"GET " + prefix + "/orders/o1": {"", "order_tracking_empty_number.json"},
		},
		http.StatusOK,
	)
	order, err := client.GetOrder(context.Background(), "t1", "o1")
	if err != nil || order.Tracking == nil || order.Tracking.Number != nil {
		t.Fatal(order, err)
	}
}
