package http

import (
	"context"
	"encoding/json"
	"log/slog"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	appcart "github.com/proshop-app/proshop-marketplace/internal/application/cart"
	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	apppromotion "github.com/proshop-app/proshop-marketplace/internal/application/promotion"
	"github.com/proshop-app/proshop-marketplace/internal/domain/cart"
	"github.com/proshop-app/proshop-marketplace/internal/domain/checkout"
	"github.com/proshop-app/proshop-marketplace/internal/domain/promotion"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
	"github.com/proshop-app/proshop-marketplace/pkg/money"
)

func TestRublesExactJSONAndBoundary(t *testing.T) {
	for _, tc := range []struct {
		kopecks int64
		json    string
	}{
		{0, "0.00"},
		{1, "0.01"},
		{10, "0.10"},
		{12345, "123.45"},
		{-12345, "-123.45"},
		{maxPublicKopecks, "9999999999999.99"},
		{-maxPublicKopecks, "-9999999999999.99"},
	} {
		raw, err := json.Marshal(rubles(tc.kopecks))
		if err != nil || string(raw) != tc.json {
			t.Fatalf("%d: %s, %v", tc.kopecks, raw, err)
		}
		var restored rubles
		if err := json.Unmarshal(raw, &restored); err != nil || restored != rubles(tc.kopecks) {
			t.Fatalf("%s: %d, %v", raw, restored, err)
		}
	}
	for _, raw := range []string{
		`"123.45"`,
		"null",
		"1.001",
		"1e2",
		"+1",
		"01",
		"NaN",
		"true",
		"[]",
		"{}",
		"10000000000000",
		"-10000000000000",
		"9223372036854775807",
		"0.000",
	} {
		var value rubles
		if err := json.Unmarshal([]byte(raw), &value); err == nil {
			t.Fatalf("принята некорректная публичная сумма %s", raw)
		}
	}
	if _, err := json.Marshal(rubles(maxPublicKopecks + 1)); err == nil {
		t.Fatal("сумма выше публичного диапазона сериализована")
	}
	// JSON домена, БД, провайдеров и индекса не меняет единицу и диапазон.
	raw, err := json.Marshal(money.FromKopecks(math.MaxInt64))
	if err != nil || string(raw) != "9223372036854775807" {
		t.Fatalf("изменился внутренний контракт: %s, %v", raw, err)
	}
}

func TestPublicRublesQueriesKeepKopecksInside(t *testing.T) {
	query, err := parseOfferQuery(httptest.NewRequest(http.MethodGet, "/?priceMin=123.45&priceMax=500", nil))
	if err != nil || query.PriceMin == nil || *query.PriceMin != 12345 ||
		query.PriceMax == nil || *query.PriceMax != 50000 {
		t.Fatalf("границы не переведены в копейки: %+v, %v", query, err)
	}
	for _, value := range []string{"123.456", "1e2", "10000000000000", "NaN"} {
		_, err := parseOfferQuery(httptest.NewRequest(http.MethodGet, "/?priceMin="+value, nil))
		if err == nil {
			t.Fatalf("принята граница %q", value)
		}
	}
}

func TestMoneyContractRejectsOldClientsBeforeMutation(t *testing.T) {
	for _, header := range []string{"", "KOPECKS", "rub", "RUB, RUB"} {
		called := false
		handler := moneyContract("/api/v1/cart/lines", func(w http.ResponseWriter, _ *http.Request) {
			called = true
			w.WriteHeader(http.StatusCreated)
		})
		request := httptest.NewRequest(http.MethodPost, "/api/v1/cart/lines", strings.NewReader(`{"quantity":2}`))
		if header != "" {
			request.Header.Set(moneyUnitHeader, header)
		}
		response := httptest.NewRecorder()
		handler(response, request)
		if called || response.Code != http.StatusConflict ||
			!strings.Contains(response.Body.String(), `"code":"api.money_unit_required"`) ||
			response.Header().Get(moneyUnitHeader) != "RUB" {
			t.Fatalf("старый клиент не остановлен: %q %d %s", header, response.Code, response.Body.String())
		}
	}
	for _, tc := range []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/api/v1/catalog/offers"},
		{http.MethodPost, "/api/v1/cart/lines"},
		{http.MethodPut, "/api/v1/operator/promotion/placements/x"},
	} {
		response := httptest.NewRecorder()
		handler := NewHandler(Deps{Registerer: prometheus.NewRegistry(), Logger: slog.Default()})
		handler.ServeHTTP(response, httptest.NewRequest(tc.method, tc.path, nil))
		if response.Code != http.StatusConflict {
			t.Fatalf("защита не подключена к маршрутам: %s %d", tc.path, response.Code)
		}
	}

	for _, tc := range []struct {
		path   string
		marker string
	}{
		{"/api/v1/cart", "RUB"},
		{"/api/v1/taxonomy/categories", "RUB"},
		{"/api/v1/internal/tenants/t1/promotion/campaigns", ""},
		{"/health", ""},
	} {
		response := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodGet, tc.path, nil)
		request.Header.Set(moneyUnitHeader, "RUB")
		moneyContract(tc.path, func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNoContent)
		})(response, request)
		if response.Code != http.StatusNoContent || response.Header().Get(moneyUnitHeader) != tc.marker {
			t.Fatalf("маркер %s: %d %v", tc.path, response.Code, response.Header())
		}
	}
}

func TestCartAndCheckoutServerBreakdown(t *testing.T) {
	line := cart.Line{
		Offer:     ids.OfferID{Tenant: "t1", Product: "p1"},
		Quantity:  3,
		Price:     money.FromKopecks(12345),
		Discounts: []cart.Discount{{Amount: money.FromKopecks(1234)}},
	}
	state := cart.State{Lines: []cart.Line{line}}
	body, err := toCartJSON(state)
	if err != nil || body.Total != 35801 || body.BaseTotal != 37035 || body.Discount != 1234 ||
		body.Lines[0].DiscountPercent != 3 || body.Groups[0].Total != 35801 || body.Groups[0].Discount != 1234 {
		t.Fatalf("корзина: %+v, %v", body, err)
	}
	raw, err := json.Marshal(body)
	if err != nil || !strings.Contains(string(raw), `"total":358.01`) ||
		!strings.Contains(string(raw), `"baseAmount":370.35`) || !strings.Contains(string(raw), `"discount":12.34`) {
		t.Fatalf("не рубли в ответе: %s, %v", raw, err)
	}
	if state.Lines[0].Price.Kopecks() != 12345 || state.Lines[0].Discounts[0].Amount.Kopecks() != 1234 {
		t.Fatal("транспорт изменил доменный снимок")
	}
	checkoutState := checkout.State{
		GoodsTotal:    money.FromKopecks(35801),
		DeliveryTotal: money.FromKopecks(123),
		Total:         money.FromKopecks(35924),
		Groups: []checkout.Group{{
			TenantID:      "t1",
			Status:        checkout.GroupPaid,
			Goods:         money.FromKopecks(35801),
			DeliveryPrice: money.FromKopecks(123),
			Lines:         []checkout.Line{{Price: line.Price, Quantity: 3, Discounts: line.Discounts}},
			CurrentPrices: []checkout.PriceChange{{ProductID: "p1", Price: money.FromKopecks(12346)}},
		}},
	}
	purchase, err := toCheckoutJSON(checkoutState)
	if err != nil || purchase.BaseTotal != 37035 || purchase.Discount != 1234 ||
		purchase.Groups[0].Total != 35924 || purchase.PaidTotal != 35924 {
		t.Fatalf("оформление: %+v, %v", purchase, err)
	}
	raw, err = json.Marshal(purchase)
	if err != nil || !strings.Contains(string(raw), `"currentPrices":[{"productId":"p1","price":123.46}]`) {
		t.Fatalf("изменения цены: %s, %v", raw, err)
	}
	_, _, percent, err := lineBreakdown(money.FromKopecks(200), 1, money.FromKopecks(199))
	if err != nil || percent != 1 {
		t.Fatalf("округление 0.5%%: %d, %v", percent, err)
	}
	overflow := cart.State{Lines: []cart.Line{{Price: money.FromKopecks(math.MaxInt64), Quantity: 2}}}
	if _, err := toCartJSON(overflow); err == nil {
		t.Fatal("не поймано переполнение количества")
	}
}

func TestPromotionPublicRublesAndInternalKopecks(t *testing.T) {
	state := apppromotion.Campaign{CampaignState: promotion.CampaignState{
		Price:   money.FromKopecks(12345),
		Payment: promotion.Payment{Amount: money.FromKopecks(23456)},
	}}
	for _, tc := range []struct {
		path     string
		contains string
		absent   string
	}{
		{"/api/v1/operator/promotion/campaigns/id", `"priceRubles":123.45`, `"priceKopecks"`},
		{"/api/v1/internal/tenants/t1/promotion/campaigns/id", `"priceKopecks":12345`, `"priceRubles"`},
	} {
		request := httptest.NewRequest(http.MethodGet, tc.path, nil)
		raw, err := json.Marshal(campaignMoneyResponse(request, state))
		if err != nil || !strings.Contains(string(raw), tc.contains) || strings.Contains(string(raw), tc.absent) {
			t.Fatalf("контракт %s: %s, %v", tc.path, raw, err)
		}
	}
	request := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/operator/promotion/campaigns/id/decisions",
		strings.NewReader(`{"action":"pay","payment":{"amountRubles":123.45}}`),
	)
	command, err := readPromotionDecision(httptest.NewRecorder(), request)
	if err != nil || command.Payment.AmountKopecks != 12345 {
		t.Fatalf("сумма команды не в копейках: %+v, %v", command, err)
	}
	request = httptest.NewRequest(
		http.MethodPost,
		"/api/v1/operator/promotion/campaigns/id/decisions",
		strings.NewReader(`{"payment":{"amountKopecks":12345}}`),
	)
	if _, err := readPromotionDecision(httptest.NewRecorder(), request); err == nil {
		t.Fatal("старое денежное поле принято публичной мутацией")
	}
}

func TestPriceFacetConvertsOnlyMoney(t *testing.T) {
	price, number := json.Number("12345"), json.Number("12.345")
	result, err := facetResponses([]ports.Facet{
		{Code: "price", Min: &price, Buckets: []ports.PriceBucket{{From: 12345, To: 23456, Count: 7}}},
		{Code: "attr.width", Min: &number, Unit: "mm"},
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(result)
	if err != nil || !strings.Contains(string(raw), `"min":123.45`) ||
		!strings.Contains(string(raw), `"min":12.345`) || !strings.Contains(string(raw), `"count":7`) {
		t.Fatalf("фасеты: %s, %v", raw, err)
	}
}

func TestMoneyEncodingFailureNeverSendsPartialSuccess(t *testing.T) {
	response := httptest.NewRecorder()
	writeJSON(response, http.StatusOK, struct {
		Price rubles `json:"price"`
	}{Price: rubles(maxPublicKopecks + 1)})
	if response.Code != http.StatusInternalServerError || !json.Valid(response.Body.Bytes()) ||
		strings.Contains(response.Body.String(), `"price"`) {
		t.Fatalf("частичный ответ: %d %s", response.Code, response.Body.String())
	}
}

func TestCartMutationsUseServerPriceAndKeepStoredKopecks(t *testing.T) {
	store, index, platform := fake.NewStore(), fake.NewIndex(), fake.NewPlatform()
	now := time.Now()
	if err := index.Put(context.Background(), []ports.OfferDocument{{
		ID:      "t1~p1",
		Visible: true,
		Price:   90000,
		Name:    "Товар",
	}}); err != nil {
		t.Fatal(err)
	}
	platform.SetItem("t1", ports.ItemCheck{
		ProductID: "p1",
		Exists:    true,
		Available: 9,
		Price:     money.FromKopecks(19999),
	})
	handler := cartHandlers{carts: appcart.NewService(
		store,
		index,
		platform,
		&fake.IDs{},
		fake.NewClock(now),
		time.Hour,
	)}
	for _, tc := range []struct {
		body   string
		status int
	}{
		{`{"offerId":"t1~p1","quantity":2,"price":0.01}`, http.StatusBadRequest},
		{`{"offerId":"t1~p1","quantity":2,"total":0.02}`, http.StatusBadRequest},
		{`{"offerId":"t1~p1","quantity":2}`, http.StatusOK},
	} {
		request := httptest.NewRequest(http.MethodPost, "/api/v1/cart/lines", strings.NewReader(tc.body))
		request = request.WithContext(context.WithValue(request.Context(), keyBuyer, ids.UUID("buyer")))
		response := httptest.NewRecorder()
		handler.add(response, request)
		if response.Code != tc.status {
			t.Fatalf("мутация: %d %s", response.Code, response.Body.String())
		}
		stored, found := store.Cart("buyer")
		if tc.status != http.StatusOK {
			if found {
				t.Fatal("недопустимая клиентская сумма изменила корзину")
			}
			continue
		}
		if !found || stored.Lines[0].Price.Kopecks() != 19999 || stored.Lines[0].Quantity != 2 ||
			!strings.Contains(response.Body.String(), `"price":199.99`) ||
			!strings.Contains(response.Body.String(), `"total":399.98`) {
			t.Fatalf("серверная цена/итог: %+v %s", stored, response.Body.String())
		}
	}
}

func TestCheckoutPaidTotalKeepsLastConfirmedStatus(t *testing.T) {
	state := checkout.State{}
	for i, status := range []checkout.GroupStatus{
		checkout.GroupPaid,
		checkout.GroupShipped,
		checkout.GroupDelivered,
		checkout.GroupAwaitingPayment,
		checkout.GroupCancelled,
		checkout.GroupReturned,
		checkout.GroupSellerUnreachable,
	} {
		group := checkout.Group{
			Status:        status,
			Goods:         money.FromKopecks(100),
			DeliveryPrice: money.FromKopecks(10),
			Lines:         []checkout.Line{{Price: money.FromKopecks(100), Quantity: 1}},
		}
		if i == 6 {
			group.OrderID = "accepted-order"
			group.LastOrderStatus = checkout.GroupPaid
		}
		state.Groups = append(state.Groups, group)
	}
	state.GoodsTotal = money.FromKopecks(700)
	state.DeliveryTotal = money.FromKopecks(70)
	state.Total = money.FromKopecks(770)
	body, err := toCheckoutJSON(state)
	if err != nil || body.PaidTotal != 440 || body.BaseTotal != 700 || body.Discount != 0 || body.Total != 770 {
		t.Fatalf("смешанные статусы: %+v, %v", body, err)
	}
}

func TestOpenAPIMoneyGuardMatchesServedRoutes(t *testing.T) {
	doc, _ := loadSpec(t)
	for _, route := range routes(Deps{}) {
		operation := doc.Paths[route.path][strings.ToLower(route.method)].(map[string]any)
		hasGuard := false
		parameters, _ := operation["parameters"].([]any)
		for _, raw := range parameters {
			parameter := raw.(map[string]any)
			if parameter["$ref"] == "#/components/parameters/MoneyUnit" {
				hasGuard = true
			}
		}
		wantGuard := publicMoneyRoute(route.method, route.path)
		if hasGuard != wantGuard {
			t.Errorf("денежный заголовок OpenAPI %s %s: %v вместо %v", route.method, route.path, hasGuard, wantGuard)
		}
		if wantGuard {
			responses := operation["responses"].(map[string]any)
			if _, documented := responses["409"]; !documented {
				t.Errorf("не описан 409 денежного контракта: %s %s", route.method, route.path)
			}
		}
	}
}
