package http

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/proshop-app/proshop-marketplace/internal/domain/checkout"
	"github.com/proshop-app/proshop-marketplace/pkg/money"
)

func TestDeliveryChangedIncludesSnapshot(t *testing.T) {
	state := checkout.State{
		Status:        checkout.StatusDraft,
		GoodsTotal:    money.FromKopecks(100),
		DeliveryTotal: money.FromKopecks(50),
		Total:         money.FromKopecks(150),
	}
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/checkout/id/orders", nil)
	(&checkoutHandlers{}).respond(response, request, http.StatusOK, state, checkout.ErrDeliveryChanged)
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
