package platformapi

import (
	"bytes"
	"encoding/json"
	"os"
	"reflect"
	"testing"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/cart"
	"github.com/proshop-app/proshop-marketplace/internal/domain/checkout"
	"github.com/proshop-app/proshop-marketplace/pkg/money"
)

// Проверяем полное тело без сокетов: лишнее поле тоже нарушает контракт.
func TestOrderRequestFixtures(t *testing.T) {
	for _, fixture := range []string{"order_create_request.json", "promo_order_request.json"} {
		t.Run(fixture, func(t *testing.T) {
			request := ports.CreateOrderRequest{
				ExternalCheckoutID: "checkout1:t1",
				GroupID:            "t1",
				Items:              []ports.OrderItem{{ProductID: "p1", Quantity: 2, Price: money.FromKopecks(199000)}},
				Carrier:            "cdek",
				PickupPointCode:    "P1",
				Contact:            checkout.Contact{Phone: "79251234567", Name: "Покупатель"},
				DeliveryPrice:      money.FromKopecks(15000),
			}
			if fixture == "promo_order_request.json" {
				request.PromoCode = "SAVE"
				request.Items[0].Discounts = []cart.Discount{{PromotionID: "promo1", Amount: money.FromKopecks(11)}}
			}
			want, err := os.ReadFile("testdata/" + fixture)
			if err != nil {
				t.Fatal(err)
			}
			got, err := json.Marshal(orderBody(request))
			if err != nil {
				t.Fatal(err)
			}
			var actual, expected any
			for target, raw := range map[*any][]byte{&actual: got, &expected: want} {
				decoder := json.NewDecoder(bytes.NewReader(raw))
				decoder.UseNumber()
				if err := decoder.Decode(target); err != nil {
					t.Fatal(err)
				}
			}
			if !reflect.DeepEqual(actual, expected) {
				t.Fatalf("запрос: %s; контракт: %s", got, want)
			}
		})
	}
}
