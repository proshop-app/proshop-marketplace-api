package http

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	appcheckout "github.com/proshop-app/proshop-marketplace/internal/application/checkout"
	appdelivery "github.com/proshop-app/proshop-marketplace/internal/application/delivery"
	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/checkout"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
	"github.com/proshop-app/proshop-marketplace/pkg/money"
)

type checkoutReadResponse struct {
	Items  []json.RawMessage       `json:"items"`
	Issues []appcheckout.ReadIssue `json:"issues"`
	Next   string                  `json:"next"`
	Error  errorDetail             `json:"error"`
}

func TestUnreadableCheckoutListHTTP(t *testing.T) {
	const (
		id    ids.UUID = "f0000000-0000-4000-8000-000000000001"
		owner ids.UUID = "f0000000-0000-4000-8000-000000000002"
		other ids.UUID = "f0000000-0000-4000-8000-000000000003"
	)
	store := fake.NewStore()
	awaitingSince := time.Date(2026, time.October, 23, 10, 0, 0, 123456000, time.UTC)
	clock := fake.NewClock(awaitingSince.Add(time.Hour))
	service := appcheckout.NewService(store, nil, nil, nil, &fake.IDs{}, clock, appcheckout.Config{})
	handlers := &checkoutHandlers{checkouts: service}
	state := checkout.State{
		ID:             id,
		BuyerID:        owner,
		IdempotencyKey: "unreadable",
		Status:         checkout.StatusAwaitingPayment,
		Groups: []checkout.Group{{
			TenantID:      "t1",
			OrderID:       "order",
			Status:        checkout.GroupAwaitingPayment,
			AwaitingSince: awaitingSince,
		}},
	}
	if err := store.Do(context.Background(), func(ctx context.Context, tx ports.Tx) error {
		return tx.Checkouts().Add(ctx, checkout.Restore(state))
	}); err != nil {
		t.Fatal(err)
	}
	store.FailCheckoutRead(id, "t1", errors.New("приватная ошибка разбора"))

	call := func(handler http.HandlerFunc, query string, buyer ids.UUID) (int, checkoutReadResponse) {
		t.Helper()
		request := httptest.NewRequest(http.MethodGet, "/"+query, nil)
		request.SetPathValue("checkoutId", string(id))
		request = request.WithContext(context.WithValue(request.Context(), keyBuyer, buyer))
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		var body checkoutReadResponse
		if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(recorder.Body.String(), "приватная ошибка разбора") {
			t.Fatal("ответ содержит внутреннюю ошибку")
		}
		return recorder.Code, body
	}

	code, panel := call(handlers.operatorList, "?stuck=true", "")
	if code != http.StatusOK || len(panel.Items) != 0 || panel.Next == "" || len(panel.Issues) != 1 {
		t.Fatalf("панель не показала признак ошибки: %d %+v", code, panel)
	}
	issue := panel.Issues[0]
	if issue.CheckoutID != id || issue.TenantID != "t1" || issue.Reason != checkout.ReadGroup {
		t.Fatalf("неожиданные данные о покупке: %+v", issue)
	}
	code, next := call(handlers.operatorList, "?stuck=true&after="+url.QueryEscape(panel.Next), "")
	if code != http.StatusOK || next.Next != "" || len(next.Issues) != 0 {
		t.Fatalf("курсор повторил уже прочитанную покупку: %d %+v", code, next)
	}
	code, old := call(handlers.operatorList, "?stuck=true&after="+string(id), "")
	if code != http.StatusBadRequest || old.Error.Code != ErrInvalidQuery.Key() {
		t.Fatalf("курсор зависших из одного ID: %d %+v", code, old)
	}
	code, cabinet := call(handlers.orders, "", owner)
	if code != http.StatusOK || cabinet.Next != string(id) || len(cabinet.Items) != 0 || len(cabinet.Issues) != 0 {
		t.Fatalf("кабинет показал повреждённую покупку или потерял курсор: %d %+v", code, cabinet)
	}
	code, one := call(handlers.get, "", owner)
	if code != http.StatusInternalServerError || one.Error.Code != checkout.ErrUnreadable.Key() ||
		one.Error.Message != checkout.ErrUnreadable.Message() {
		t.Fatalf("ответ на чтение одной покупки: %d %+v", code, one)
	}
	code, foreign := call(handlers.get, "", other)
	if code != http.StatusNotFound || foreign.Error.Code != checkout.ErrNotFound.Key() {
		t.Fatalf("ответ на чтение чужой покупки: %d %+v", code, foreign)
	}
	delivery := &deliveryHandlers{
		directory: appdelivery.NewService(store, nil, nil, appdelivery.Config{}),
		checkouts: service,
	}
	code, points := call(delivery.checkoutPickupPoints, "?carrier=cdek&city=City", owner)
	if code != http.StatusInternalServerError || points.Error.Code != checkout.ErrUnreadable.Key() {
		t.Fatalf("ответ на чтение пунктов выдачи покупки: %d %+v", code, points)
	}
	code, foreign = call(delivery.checkoutPickupPoints, "?carrier=cdek&city=City", other)
	if code != http.StatusNotFound || foreign.Error.Code != checkout.ErrNotFound.Key() {
		t.Fatalf("ответ на чтение пунктов выдачи чужой покупки: %d %+v", code, foreign)
	}
}

func TestCheckoutListSkipsInvalidSnapshotHTTP(t *testing.T) {
	store := fake.NewStore()
	service := appcheckout.NewService(store, nil, nil, nil, nil, nil, appcheckout.Config{})
	handlers := &checkoutHandlers{checkouts: service}
	items := []checkout.State{
		{ID: "healthy-before"},
		{
			ID:         "invalid-totals",
			GoodsTotal: money.FromKopecks(1),
			Groups:     []checkout.Group{{TenantID: "t1"}},
		},
		{ID: "healthy-after"},
		{
			ID:        "invalid-date",
			CreatedAt: time.Date(10000, time.January, 1, 0, 0, 0, 0, time.UTC),
		},
	}
	page := appcheckout.ListPage[checkout.State]{
		Items: items,
		Next:  checkout.ListCursor{ID: items[3].ID},
	}
	for _, operator := range []bool{true, false} {
		if !operator {
			page.Issues = []appcheckout.ReadIssue{{CheckoutID: "skipped", Reason: checkout.ReadGroup}}
		}
		request := httptest.NewRequest(http.MethodGet, "/", nil)
		recorder := httptest.NewRecorder()
		handlers.respondList(recorder, request, page, operator)
		var body checkoutReadResponse
		if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if recorder.Code != http.StatusOK || len(body.Items) != 2 || body.Next != string(page.Next.ID) {
			t.Fatalf("ошибка преобразования снимка прервала страницу: %s", recorder.Body.String())
		}
		for i, expected := range []checkout.State{items[0], items[2]} {
			var item checkoutJSON
			if err := json.Unmarshal(body.Items[i], &item); err != nil || item.ID != string(expected.ID) {
				t.Fatalf("покупка %d: %s, %v", i, body.Items[i], err)
			}
		}
		if operator && (len(body.Issues) != 2 || body.Issues[0].CheckoutID != items[1].ID ||
			body.Issues[1].CheckoutID != items[3].ID) {
			t.Fatalf("оператор не увидел ошибку: %s", recorder.Body.String())
		}
		if !operator && strings.Contains(recorder.Body.String(), `"issues"`) {
			t.Fatalf("покупатель увидел диагностику: %s", recorder.Body.String())
		}
	}
}

func TestCheckoutGetClassifiesInvalidSnapshotHTTP(t *testing.T) {
	const id ids.UUID = "f0000000-0000-4000-8000-000000000001"
	const owner ids.UUID = "f0000000-0000-4000-8000-000000000002"
	now := time.Date(2026, time.October, 23, 10, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name      string
		createdAt time.Time
		goods     money.Money
	}{
		{name: "итоги", createdAt: now, goods: money.FromKopecks(1)},
		{name: "дата вне JSON", createdAt: now.AddDate(8000, 0, 0)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := fake.NewStore()
			service := appcheckout.NewService(store, nil, nil, nil, nil, fake.NewClock(now), appcheckout.Config{})
			handlers := &checkoutHandlers{checkouts: service}
			state := checkout.State{
				ID:             id,
				BuyerID:        owner,
				IdempotencyKey: "invalid-snapshot",
				CreatedAt:      tc.createdAt,
				GoodsTotal:     tc.goods,
				Groups:         []checkout.Group{{TenantID: "t1"}},
			}
			if err := store.Do(context.Background(), func(ctx context.Context, tx ports.Tx) error {
				return tx.Checkouts().Add(ctx, checkout.Restore(state))
			}); err != nil {
				t.Fatal(err)
			}
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.SetPathValue("checkoutId", string(id))
			record := httptest.NewRecorder()
			handlers.get(record, withBuyer(req, owner))
			var body checkoutReadResponse
			if err := json.Unmarshal(record.Body.Bytes(), &body); err != nil ||
				record.Code != http.StatusInternalServerError || body.Error.Code != checkout.ErrUnreadable.Key() {
				t.Fatalf("повреждённый снимок: %d %s", record.Code, record.Body)
			}
		})
	}
}
