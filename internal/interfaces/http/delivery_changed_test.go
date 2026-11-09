package http

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	appcheckout "github.com/proshop-app/proshop-marketplace/internal/application/checkout"
	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	"github.com/proshop-app/proshop-marketplace/internal/domain/checkout"
	"github.com/proshop-app/proshop-marketplace/pkg/money"
)

func TestDeliveryChangedIncludesSnapshot(t *testing.T) {
	state := checkout.State{
		Groups: []checkout.Group{{
			Goods: money.FromKopecks(100),
			Lines: []checkout.Line{{Price: money.FromKopecks(100), Quantity: 1}},
		}},
		Status:        checkout.StatusDraft,
		GoodsTotal:    money.FromKopecks(100),
		DeliveryTotal: money.FromKopecks(50),
		Total:         money.FromKopecks(150),
	}
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/checkout/id/orders", nil)
	request.Header.Set("X-Marketplace-Money-Unit", "RUB")
	service := appcheckout.NewService(fake.NewStore(), nil, nil, nil, nil, nil, nil, appcheckout.Config{})
	handler := checkoutHandlers{checkouts: service}
	handler.respond(response, request, http.StatusOK, state, checkout.ErrDeliveryChanged)
	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
		Checkout checkoutJSON `json:"checkout"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusConflict || body.Error.Code != "DELIVERY_CHANGED" || body.Checkout.Total != 150 {
		t.Fatalf("изменённая доставка: %d %s", response.Code, response.Body.String())
	}
}
