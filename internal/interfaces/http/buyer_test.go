package http_test

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/buyer"
	"github.com/proshop-app/proshop-marketplace/internal/domain/delivery"
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
	transport "github.com/proshop-app/proshop-marketplace/internal/interfaces/http"
	"github.com/proshop-app/proshop-marketplace/pkg/money"
)

// session — запрос с кукой сессии покупателя.
func (e *env) session(t *testing.T, method, path, body, cookie string, headers ...string) response {
	t.Helper()

	request, err := http.NewRequest(method, e.server.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if cookie != "" {
		request.AddCookie(&http.Cookie{Name: transport.SessionCookie, Value: cookie})
	}
	for i := 0; i+1 < len(headers); i += 2 {
		request.Header.Set(headers[i], headers[i+1])
	}

	raw, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = raw.Body.Close() }()

	out := response{status: raw.StatusCode, header: raw.Header}
	data, _ := io.ReadAll(raw.Body)
	if len(data) > 0 && strings.HasPrefix(raw.Header.Get("Content-Type"), "application/json") {
		out.body = decode(t, data)
	} else if len(data) > 0 {
		out.body = map[string]any{"text": string(data)}
	}

	return out
}

// login проходит вход по звонку и отдаёт токен из куки.
func (e *env) login(t *testing.T) string {
	t.Helper()

	started := e.session(t, http.MethodPost, "/api/v1/auth/call", `{"phone":"+7 925 123-45-67"}`, "")
	if started.status != http.StatusCreated || started.body["callPhonePretty"] != "+7 (800) 500-8275" ||
		started.body["status"] != "pending" {
		t.Fatalf("начало входа: %+v", started)
	}

	challenge := started.body["challengeId"].(string)

	pending := e.session(t, http.MethodPost, "/api/v1/auth/call/check", `{"challengeId":"`+challenge+`"}`, "")
	if pending.status != http.StatusOK || pending.body["status"] != "pending" ||
		pending.header.Get("Set-Cookie") != "" {
		t.Fatalf("звонка ещё не было: %+v", pending)
	}

	form := url.Values{"data[0]": {"callcheck_status\nref-1\n401\n1500000000"}}
	e.calls.Set("ref-1", buyer.CallConfirmed)

	ack, err := http.PostForm(e.server.URL+"/api/v1/auth/webhooks/smsru", form)
	if err != nil {
		t.Fatal(err)
	}
	text, _ := io.ReadAll(ack.Body)
	_ = ack.Body.Close()
	if string(text) != "100" {
		t.Fatalf("ответ шлюзу: %q", text)
	}

	confirmed := e.session(t, http.MethodPost, "/api/v1/auth/call/check", `{"challengeId":"`+challenge+`"}`, "")
	if confirmed.status != http.StatusOK || confirmed.body["status"] != "confirmed" || confirmed.body["buyer"] == nil {
		t.Fatalf("вход состоялся: %+v", confirmed)
	}
	if _, leaked := confirmed.body["token"]; leaked {
		t.Fatal("токен сессии не уходит в тело ответа")
	}

	var cookie *http.Cookie
	for _, c := range (&http.Response{Header: confirmed.header}).Cookies() {
		if c.Name == transport.SessionCookie {
			cookie = c
		}
	}
	if cookie == nil || !cookie.Secure || !cookie.HttpOnly || cookie.SameSite != http.SameSiteLaxMode ||
		cookie.Path != "/" {
		t.Fatalf("кука __Host-: %+v", cookie)
	}

	return cookie.Value
}

func TestLoginFlow(t *testing.T) {
	e := newEnv(t)
	token := e.login(t)

	r := e.session(t, http.MethodGet, "/api/v1/me", "", "")
	if r.status != http.StatusUnauthorized || errorCode(r) != "buyer.unauthorized" {
		t.Fatalf("без сессии: %+v", r)
	}

	r = e.session(t, http.MethodGet, "/api/v1/me", "", token)
	if r.status != http.StatusOK || r.body["phone"] != "79251234567" {
		t.Fatalf("покупатель сессии: %+v", r)
	}

	r = e.session(t, http.MethodGet, "/api/v1/me", "", "", "Authorization", "Bearer "+token)
	if r.status != http.StatusOK {
		t.Fatalf("сессия заголовком для серверного рендера: %+v", r)
	}

	r = e.session(t, http.MethodPost, "/api/v1/auth/call", `{"phone":"12"}`, "")
	if r.status != http.StatusBadRequest || errorCode(r) != "buyer.invalid_phone" {
		t.Fatalf("неверный номер: %+v", r)
	}

	out := e.session(t, http.MethodPost, "/api/v1/auth/logout", "", token)
	if out.status != http.StatusNoContent || !strings.Contains(out.header.Get("Set-Cookie"), "Max-Age=0") {
		t.Fatalf("выход стирает куку: %+v", out)
	}
	if r := e.session(t, http.MethodGet, "/api/v1/me", "", token); r.status != http.StatusUnauthorized {
		t.Fatalf("после выхода: %+v", r)
	}
}

func TestLoginRateLimit(t *testing.T) {
	e := newEnv(t)

	for range 10 {
		e.session(t, http.MethodPost, "/api/v1/auth/call", `{"phone":"79251234567"}`, "")
	}

	r := e.session(t, http.MethodPost, "/api/v1/auth/call", `{"phone":"79251234567"}`, "")
	if r.status != http.StatusTooManyRequests || errorCode(r) != "buyer.too_many_attempts" {
		t.Fatalf("лимит попыток — 429: %+v", r)
	}
}

func TestCartDeliveryCheckout(t *testing.T) {
	e := newEnv(t)
	token := e.login(t)
	ctx := context.Background()

	e.store.SeedParticipant(participant.State{
		TenantID:         "t1",
		Slug:             "s1",
		Name:             "Магазин",
		Status:           participant.StatusConnected,
		PaymentAccountID: "acc",
		Enabled:          true,
		ShopEnabled:      true,
		Version:          1,
		CreatedAt:        t0,
		UpdatedAt:        t0,
	})

	offer := ports.OfferDocument{
		ID:                "t1~p1",
		TenantID:          "t1",
		ProductID:         "p1",
		Name:              "Чайник",
		Price:             199000,
		Available:         3,
		Visible:           true,
		Listed:            true,
		CheckoutAvailable: true,
		Version:           1,
	}
	_ = e.index.Put(ctx, []ports.OfferDocument{offer})

	point, _ := delivery.NewPickupPoint(
		"cdek",
		"MSK1",
		"Пункт",
		"ул. Примерная, 1",
		"Город",
		delivery.Location{Lat: 55.7, Lon: 37.6},
		"Пн-Пт",
		nil,
		t0,
	)
	e.store.SeedPickupPoint(point)
	_ = e.pickups.Put(ctx, []delivery.PickupPoint{point})
	e.platform.SetItem(
		"t1",
		ports.ItemCheck{ProductID: "p1", Exists: true, Price: money.FromKopecks(199000), Available: 3},
	)
	e.platform.SetQuote("t1", delivery.Offered{Available: true, Cost: money.FromKopecks(30000), MinDays: 2, MaxDays: 4})

	if r := e.session(t, http.MethodGet, "/api/v1/cart", "", ""); r.status != http.StatusUnauthorized {
		t.Fatalf("корзина только с сессией: %+v", r)
	}

	added := e.session(t, http.MethodPost, "/api/v1/cart/lines", `{"offerId":"t1~p1","quantity":2}`, token)
	if added.status != http.StatusOK || added.body["total"] != float64(398000) {
		t.Fatalf("добавление: %+v", added)
	}

	r := e.session(t, http.MethodPost, "/api/v1/cart/lines", `{"offerId":"t1~p1","quantity":5}`, token)
	if r.status != http.StatusConflict || errorCode(r) != "cart.not_enough_stock" {
		t.Fatalf("сверх остатка: %+v", r)
	}

	r = e.session(t, http.MethodPut, "/api/v1/cart/lines/t1~p1", `{"quantity":1}`, token)
	if r.status != http.StatusOK || r.body["total"] != float64(199000) {
		t.Fatalf("количество: %+v", r)
	}
	if r := e.session(t, http.MethodPost, "/api/v1/cart/verify", "", token); r.status != http.StatusOK {
		t.Fatalf("перепроверка: %+v", r)
	}

	carriers := e.session(t, http.MethodGet, "/api/v1/delivery/carriers", "", "")
	if items, _ := carriers.body["items"].([]any); carriers.status != http.StatusOK || len(items) != 1 {
		t.Fatalf("перевозчики: %+v", carriers)
	}
	points := e.session(
		t,
		http.MethodGet,
		"/api/v1/delivery/pickup-points?carrier=cdek&bbox=37.5,55.6,37.8,55.9",
		"",
		"",
	)
	if items, _ := points.body["items"].([]any); points.status != http.StatusOK || len(items) != 1 {
		t.Fatalf("пункты в области: %+v", points)
	}

	r = e.session(t, http.MethodGet, "/api/v1/delivery/pickup-points?carrier=cdek&bbox=1,2,3", "", "")
	if r.status != http.StatusBadRequest || errorCode(r) != "delivery.invalid_area" {
		t.Fatalf("неверная область: %+v", r)
	}

	r = e.session(t, http.MethodGet, "/api/v1/delivery/pickup-points?carrier=ozon&city=Город", "", "")
	if r.status != http.StatusNotFound || errorCode(r) != "delivery.unknown_carrier" {
		t.Fatalf("неподключённый перевозчик: %+v", r)
	}

	r = e.session(t, http.MethodPost, "/api/v1/checkout", "", token)
	if r.status != http.StatusBadRequest || errorCode(r) != "checkout.invalid_idempotency_key" {
		t.Fatalf("без ключа идемпотентности: %+v", r)
	}

	created := e.session(t, http.MethodPost, "/api/v1/checkout", "", token, "Idempotency-Key", "checkout-key-0001")
	if created.status != http.StatusCreated || created.body["status"] != "draft" ||
		created.body["goodsTotal"] != float64(199000) {
		t.Fatalf("покупка: %+v", created)
	}
	id := created.body["id"].(string)

	again := e.session(t, http.MethodPost, "/api/v1/checkout", "", token, "Idempotency-Key", "checkout-key-0001")
	if again.body["id"] != id {
		t.Fatalf("тот же ключ — та же покупка: %+v", again)
	}

	quoted := e.session(
		t,
		http.MethodPost,
		"/api/v1/checkout/"+id+"/delivery/quotes",
		`{"carrier":"cdek","pickupPointCode":"MSK1"}`,
		token,
	)
	groups, _ := quoted.body["groups"].([]any)
	if quoted.status != http.StatusOK || len(groups) != 1 || quoted.body["deliveryTotal"] != float64(30000) {
		t.Fatalf("расчёт доставки: %+v", quoted)
	}

	confirmed := e.session(t, http.MethodPost, "/api/v1/checkout/"+id+"/confirm", "", token)
	if confirmed.status != http.StatusOK || confirmed.body["status"] != "confirmed" ||
		confirmed.body["total"] != float64(229000) ||
		confirmed.body["confirmedAt"] == nil {
		t.Fatalf("подтверждение: %+v", confirmed)
	}

	r = e.session(t, http.MethodGet, "/api/v1/checkout/"+id, "", token)
	if r.status != http.StatusOK || r.body["status"] != "confirmed" {
		t.Fatalf("покупка после подтверждения: %+v", r)
	}
	if r := e.session(t, http.MethodGet, "/api/v1/checkout/not-a-uuid", "", token); r.status != http.StatusNotFound {
		t.Fatalf("чужой формат идентификатора: %+v", r)
	}

}
