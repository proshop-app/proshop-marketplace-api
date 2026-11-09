package http

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	appbuyer "github.com/proshop-app/proshop-marketplace/internal/application/buyer"
	appcheckout "github.com/proshop-app/proshop-marketplace/internal/application/checkout"
	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/buyer"
	"github.com/proshop-app/proshop-marketplace/internal/domain/checkout"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
	"github.com/proshop-app/proshop-marketplace/pkg/money"
)

const paymentTestID = ids.UUID("f0000000-0000-4000-8000-000000000001")
const paymentTestBuyer = ids.UUID("f0000000-0000-4000-8000-000000000002")

type paymentTestEnv struct {
	clock    *fake.Clock
	tokens   *fake.IdentityTokens
	platform *fake.Platform
	service  *appcheckout.Service
	handler  http.Handler
	store    *fake.Store
	cookie   string
	foreign  string
}

func newPaymentTest(t *testing.T, enabled bool) paymentTestEnv {
	t.Helper()
	ctx := context.Background()
	clock := fake.NewClock(time.Date(2026, 11, 7, 0, 0, 0, 0, time.UTC))
	store, platform := fake.NewStore(), fake.NewPlatform()
	order, err := platform.CreateOrder(ctx, "t1", ports.CreateOrderRequest{
		ExternalCheckoutID: string(paymentTestID),
		GroupID:            "t1",
		Items:              []ports.OrderItem{{ProductID: "p1", Quantity: 1, Price: money.FromKopecks(12345)}},
		DeliveryPrice:      money.FromKopecks(100),
	})
	if err != nil {
		t.Fatal(err)
	}
	state := checkout.State{
		ID:             paymentTestID,
		BuyerID:        paymentTestBuyer,
		IdempotencyKey: "payment-test-key",
		Status:         checkout.StatusAwaitingPayment,
		Version:        1,
		Total:          money.FromKopecks(12445),
		Groups: []checkout.Group{{
			TenantID:      "t1",
			ExpiresAt:     clock.Now().Add(time.Hour),
			OrderID:       order.OrderID,
			Status:        checkout.GroupAwaitingPayment,
			Goods:         money.FromKopecks(12345),
			DeliveryPrice: money.FromKopecks(100),
		}},
	}
	if err := store.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		return tx.Checkouts().Add(ctx, checkout.Restore(state))
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		if err := tx.Buyers().Add(ctx, buyer.New(paymentTestBuyer, "79000000001", clock.Now())); err != nil {
			return err
		}
		return tx.Buyers().Add(ctx, buyer.New("f0000000-0000-4000-8000-000000000003", "79000000002", clock.Now()))
	}); err != nil {
		t.Fatal(err)
	}
	tokens := fake.NewIdentityTokens(clock)
	owner, _ := tokens.Open(ctx, string(paymentTestBuyer))
	foreign, _ := tokens.Open(ctx, "f0000000-0000-4000-8000-000000000003")
	auth := appbuyer.NewAuth(store, nil, tokens, nil, nil, clock, appbuyer.AuthConfig{})
	service := appcheckout.NewService(store, nil, platform, nil, nil, &fake.IDs{}, clock, appcheckout.Config{
		PaymentStub:   enabled,
		ReturnBaseURL: "https://market.example",
	})
	mux := http.NewServeMux()
	for _, route := range routes(Deps{Auth: auth, Checkouts: service}) {
		if strings.Contains(route.path, "payment-stub") || strings.HasSuffix(route.path, "/pay") {
			mux.HandleFunc(route.method+" "+route.path, route.handler)
		}
	}
	return paymentTestEnv{
		handler:  mux,
		store:    store,
		cookie:   owner.Token,
		foreign:  foreign.Token,
		clock:    clock,
		tokens:   tokens,
		platform: platform,
		service:  service,
	}
}

func (e paymentTestEnv) call(method, path, cookie, origin string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, "https://market.example"+path, nil)
	request.Header.Set("X-Marketplace-Money-Unit", "RUB")
	if cookie != "" {
		request.AddCookie(&http.Cookie{Name: SessionCookie, Value: cookie})
	}
	if origin != "" {
		request.Header.Set("Origin", origin)
	}
	response := httptest.NewRecorder()
	e.handler.ServeHTTP(response, request)
	return response
}

func TestPaymentStubHTTP(t *testing.T) {
	e := newPaymentTest(t, true)
	page := "/api/v1/payment-stub/" + string(paymentTestID)
	pay := "/api/v1/checkout/" + string(paymentTestID) + "/pay"
	opened := e.call(http.MethodPost, pay, e.cookie, "https://market.example")
	var link map[string]string
	if err := json.Unmarshal(opened.Body.Bytes(), &link); err != nil {
		t.Fatal(err)
	}
	if opened.Code != 200 || link["url"] != page {
		t.Fatal(opened.Body.String())
	}
	html := e.call(http.MethodGet, page, e.cookie, "")
	for _, expected := range []string{
		"124,45",
		`method="post"`,
		page + "/confirm",
		page + "/cancel",
		"Оплатить",
		"Отказаться",
	} {
		if html.Code != 200 || !strings.Contains(html.Body.String(), expected) {
			t.Fatal(html.Body.String())
		}
	}
	for _, suffix := range []string{"/cancel", "/confirm", "/confirm"} {
		reply := e.call(http.MethodPost, page+suffix, e.cookie, "https://market.example")
		want := "https://market.example/checkout/" + string(paymentTestID) + "/return"
		if reply.Code != 303 || reply.Header().Get("Location") != want {
			t.Fatal(reply.Code, reply.Body.String())
		}
		state, _ := e.store.Checkout(paymentTestID)
		if suffix == "/cancel" && state.Status != checkout.StatusAwaitingPayment {
			t.Fatal(state)
		}
		if suffix == "/confirm" && state.Status != checkout.StatusPaid {
			t.Fatal(state)
		}
	}
	if reply := e.call(http.MethodPost, pay, e.cookie, ""); reply.Code != 409 {
		t.Fatal(reply.Code)
	}
}

func TestPaymentStubAuthorizationFlagAndCSRF(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		e := newPaymentTest(t, enabled)
		base := "/api/v1/payment-stub/" + string(paymentTestID)
		for _, route := range []struct{ method, path string }{
			{http.MethodPost, "/api/v1/checkout/" + string(paymentTestID) + "/pay"},
			{http.MethodGet, base},
			{http.MethodPost, base + "/confirm"},
			{http.MethodPost, base + "/cancel"},
		} {
			for _, check := range []struct {
				cookie string
				status int
			}{
				{"", 401},
				{e.foreign, 404},
			} {
				reply := e.call(route.method, route.path, check.cookie, "")
				expected := check.status
				if route.method == http.MethodPost && strings.Contains(route.path, "payment-stub") {
					expected = 303
				}
				if reply.Code != expected {
					t.Fatal(route, reply.Code, reply.Body.String())
				}
			}
			if !enabled {
				reply := e.call(route.method, route.path, e.cookie, "")
				expected := 503
				if route.method == http.MethodPost && strings.Contains(route.path, "payment-stub") {
					expected = 303
				}
				if reply.Code != expected {
					t.Fatal(reply.Code, reply.Body.String())
				}
			}
			if route.method == http.MethodPost {
				reply := e.call(route.method, route.path, e.cookie, "https://foreign.example")
				expected := 403
				if strings.Contains(route.path, "payment-stub") {
					expected = 303
				}
				if reply.Code != expected {
					t.Fatal(reply.Code)
				}
			}
		}
		for _, suffix := range []string{"/confirm", "/cancel"} {
			reply := e.call(http.MethodGet, base+suffix, e.cookie, "")
			if reply.Code != 405 {
				t.Fatal(reply.Code)
			}
		}
		state, _ := e.store.Checkout(paymentTestID)
		if state.Status != checkout.StatusAwaitingPayment {
			t.Fatal("отказ изменил покупку")
		}
	}
}

func TestCheckoutHasNoSellerPaymentLinks(t *testing.T) {
	body, err := toCheckoutJSON(checkout.State{Groups: []checkout.Group{{TenantID: "t1"}}})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"paymentUrl", "nextPayment", "paymentOrder"} {
		if strings.Contains(string(raw), field) {
			t.Fatal(string(raw))
		}
	}
}

func TestPaymentStubPaidWithFailedPoll(t *testing.T) {
	e := newPaymentTest(t, true)
	page := "/api/v1/payment-stub/" + string(paymentTestID)
	if r := e.call(http.MethodPost, page+"/confirm", e.cookie, ""); r.Code != 303 {
		t.Fatal(r.Code)
	}
	e.platform.Fail("t1", errors.New("нет связи"))
	for range checkout.MaxOrderFailures {
		state, err := e.service.Refresh(context.Background(), paymentTestBuyer, paymentTestID)
		if err != nil {
			t.Fatal(err)
		}
		if state.Groups[0].PollFailures < checkout.MaxOrderFailures {
			e.clock.Advance(state.Groups[0].NextPollAt.Sub(e.clock.Now()))
		}
	}
	access, err := e.tokens.Open(context.Background(), string(paymentTestBuyer))
	if err != nil {
		t.Fatal(err)
	}
	e.cookie = access.Token
	r := e.call(http.MethodGet, page, e.cookie, "")
	if r.Code != 200 || !strings.Contains(r.Body.String(), "Покупка оплачена") ||
		strings.Contains(r.Body.String(), "<form") {
		t.Fatal(r.Code, r.Body.String())
	}
	if r := e.call(http.MethodPost, page+"/confirm", e.cookie, ""); r.Code != 303 {
		t.Fatal(r.Code)
	}
}

func TestPaymentStubErrorsStayInBrowser(t *testing.T) {
	for _, mode := range []string{"server", "expired", "access"} {
		t.Run(mode, func(t *testing.T) {
			e := newPaymentTest(t, true)
			page := "/api/v1/payment-stub/" + string(paymentTestID)
			switch mode {
			case "server":
				e.platform.Fail("t1", errors.New("нет связи"))
			case "expired":
				e.clock.Advance(time.Hour)
				access, err := e.tokens.Open(context.Background(), string(paymentTestBuyer))
				if err != nil {
					t.Fatal(err)
				}
				e.cookie = access.Token
			case "access":
				e.clock.Advance(24 * time.Hour)
			}
			if mode != "server" {
				r := e.call(http.MethodGet, page, e.cookie, "")
				if r.Code < 400 || !strings.HasPrefix(r.Header().Get("Content-Type"), "text/html") ||
					!strings.Contains(r.Body.String(), "Вернуться к покупке") {
					t.Fatal(r.Code, r.Body.String())
				}
			}
			for _, suffix := range []string{"/confirm", "/cancel"} {
				r := e.call(http.MethodPost, page+suffix, e.cookie, "")
				want := "https://market.example/checkout/" + string(paymentTestID) + "/return"
				if r.Code != 303 || r.Header().Get("Location") != want {
					t.Fatal(r.Code, r.Body.String())
				}
			}
			state, _ := e.store.Checkout(paymentTestID)
			if state.Status != checkout.StatusAwaitingPayment {
				t.Fatal(state)
			}
		})
	}
}

func TestPaymentStubConcurrentConfirmRedirects(t *testing.T) {
	e := newPaymentTest(t, true)
	page := "/api/v1/payment-stub/" + string(paymentTestID)
	e.platform.AfterStubPayment = func() {
		e.platform.AfterStubPayment = nil
		r := e.call(http.MethodPost, page+"/confirm", e.cookie, "")
		if r.Code != 303 {
			t.Fatal(r.Code, r.Body.String())
		}
	}
	r := e.call(http.MethodPost, page+"/confirm", e.cookie, "")
	if r.Code != 303 {
		t.Fatal(r.Code, r.Body.String())
	}
	state, _ := e.store.Checkout(paymentTestID)
	if !checkout.Restore(state).PaymentComplete() {
		t.Fatal(state)
	}
}
