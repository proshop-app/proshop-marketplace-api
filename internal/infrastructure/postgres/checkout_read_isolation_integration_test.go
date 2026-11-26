//go:build integration

package postgres_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/prometheus/client_golang/prometheus"

	appbuyer "github.com/proshop-app/proshop-marketplace/internal/application/buyer"
	appcheckout "github.com/proshop-app/proshop-marketplace/internal/application/checkout"
	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/checkout"
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/postgres"
	transport "github.com/proshop-app/proshop-marketplace/internal/interfaces/http"
	"github.com/proshop-app/proshop-marketplace/internal/interfaces/jobs"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

const (
	breakCheckoutHeader = `UPDATE checkouts SET order_context = '"сломано"' WHERE id = $1`
	breakCheckoutTime   = `UPDATE checkouts SET updated_at = 'infinity' WHERE id = $1`
	breakCheckoutLines  = `UPDATE checkout_groups SET lines = '"сломано"' WHERE checkout_id = $1 AND tenant_id = 't1'`
	breakNextWork       = `UPDATE checkout_groups SET next_work_at = 'infinity'
		WHERE checkout_id = $1 AND tenant_id = 't1'`
	breakStuckCursor = `UPDATE checkout_groups SET stuck_at = '-infinity'
		WHERE checkout_id = $1 AND stuck_at IS NOT NULL`
	markSecondStuckGroup = `UPDATE checkout_groups SET stuck_at = $2 WHERE checkout_id = $1 AND tenant_id = 't2'`
	breakCheckoutTotal   = `UPDATE checkouts SET goods_total = goods_total + 1 WHERE id = $1`
	setSellerDelivery    = `UPDATE participants SET delivery_settings = $2::jsonb WHERE tenant_id = $1`
	deleteCheckoutGroups = `DELETE FROM checkout_groups WHERE checkout_id = $1`
	breakAwaitingSince   = `UPDATE checkout_groups
		SET order_progress = jsonb_set(order_progress, '{AwaitingSince}', '"сломано"')
		WHERE checkout_id = $1 AND tenant_id = 't1'`
)

func TestOperatorStuckPageAfterRejectedSecondMarkPostgres(t *testing.T) {
	ctx := context.Background()
	pool := testPool(t)
	uow := postgres.NewUnitOfWork(pool)
	purchases := seedCheckoutList(t, uow)
	_, err := pool.Exec(ctx, markSecondStuckGroup, purchases[3].ID(), at(10))
	var violation *pgconn.PgError
	if !errors.As(err, &violation) || violation.Code != "23505" ||
		violation.ConstraintName != "checkout_groups_one_stuck_idx" {
		t.Fatalf("ожидался отказ для второй метки зависания: %v", err)
	}

	service := appcheckout.NewService(uow, nil, nil, nil, nil, fake.NewClock(at(50)), appcheckout.Config{})
	handler := transport.NewHandler(transport.Deps{
		Checkouts:              service,
		OperatorToken:          "test-operator-token",
		RequestTimeout:         time.Second,
		ReferenceImportTimeout: time.Second,
		Registerer:             prometheus.NewRegistry(),
		Logger:                 slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/operator/checkouts?stuck=true", nil)
	req.Header.Set("X-Marketplace-Money-Unit", "RUB")
	req.Header.Set("Authorization", "Bearer test-operator-token")
	record := httptest.NewRecorder()
	handler.ServeHTTP(record, req)
	var page struct {
		Items  []struct{ ID ids.UUID }
		Issues []appcheckout.ReadIssue
	}
	if err := json.Unmarshal(record.Body.Bytes(), &page); err != nil || record.Code != http.StatusOK ||
		len(page.Issues) != 0 || len(page.Items) != 2 ||
		page.Items[0].ID != purchases[3].ID() || page.Items[1].ID != purchases[4].ID() {
		t.Fatalf("страница зависших покупок: %d %s, %v", record.Code, record.Body, err)
	}
}

func TestCheckoutReadIsolationPostgres(t *testing.T) {
	for _, tc := range []struct {
		name    string
		sql     string
		tenants []ids.TenantID
		reason  string
	}{
		{name: "группа", sql: breakCheckoutLines, tenants: []ids.TenantID{"t1"}, reason: checkout.ReadGroup},
		{name: "дата ожидания", sql: breakAwaitingSince, tenants: []ids.TenantID{"t1"}, reason: checkout.ReadGroup},
		{name: "срок работы", sql: breakNextWork, tenants: []ids.TenantID{"t1"}, reason: checkout.ReadGroup},
		{name: "покупка", sql: breakCheckoutHeader, tenants: []ids.TenantID{"t1", "t2"}, reason: checkout.ReadHeader},
		{
			name:    "дата покупки",
			sql:     breakCheckoutTime,
			tenants: []ids.TenantID{"t1", "t2"},
			reason:  checkout.ReadHeader,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			pool := testPool(t)
			uow := postgres.NewUnitOfWork(pool)
			purchases := seedCheckoutList(t, uow)
			broken := purchases[3].ID()
			if _, err := pool.Exec(ctx, tc.sql, string(broken)); err != nil {
				t.Fatal(err)
			}
			clock := fake.NewClock(at(50))
			service := appcheckout.NewService(uow, nil, nil, nil, &fake.IDs{}, clock, appcheckout.Config{})
			page, err := service.OperatorList(ctx, checkout.ListCursor{}, false)
			if err != nil || len(page.Items) != 2 || len(page.Issues) != len(tc.tenants) ||
				page.Items[0].ID != purchases[2].ID() || page.Items[1].ID != purchases[4].ID() {
				t.Fatalf("страница покупок: %+v, %v", page, err)
			}
			for i, failure := range page.Issues {
				if failure.CheckoutID != broken || failure.TenantID != tc.tenants[i] ||
					failure.Reason != tc.reason {
					t.Fatalf("признак покупки: %+v", failure)
				}
			}

			stuck, err := service.OperatorList(ctx, checkout.ListCursor{}, true)
			if err != nil || len(stuck.Items) != 1 || stuck.Items[0].ID != purchases[4].ID() ||
				len(stuck.Issues) != len(tc.tenants) {
				t.Fatalf("зависшие покупки: %+v, %v", stuck, err)
			}
			if _, err := service.Get(ctx, buyerA, broken); !errors.Is(err, checkout.ErrUnreadable) {
				t.Fatalf("ошибка чтения покупки: %v", err)
			}
			if _, err := service.Get(ctx, buyerB, broken); !errors.Is(err, checkout.ErrNotFound) {
				t.Fatalf("ошибка чтения чужой покупки: %v", err)
			}
			cabinet, err := service.Purchases(ctx, buyerA, "")
			if err != nil || len(cabinet.Items) != 0 || len(cabinet.Issues) != 0 || cabinet.Next.ID != broken {
				t.Fatalf("покупки владельца: %+v, %v", cabinet, err)
			}
		})
	}
}

func TestCheckoutReadReportsMissingGroupsOfSavedVersion(t *testing.T) {
	ctx := context.Background()
	pool := testPool(t)
	uow := postgres.NewUnitOfWork(pool)
	purchases := seedCheckoutList(t, uow)
	broken := purchases[2].ID()
	if _, err := pool.Exec(ctx, deleteCheckoutGroups, string(broken)); err != nil {
		t.Fatal(err)
	}
	service := appcheckout.NewService(uow, nil, nil, nil, nil, nil, appcheckout.Config{})
	page, err := service.OperatorList(ctx, checkout.ListCursor{}, false)
	if err != nil || len(page.Items) != 2 || page.Next.ID != purchases[4].ID() || len(page.Issues) != 1 {
		t.Fatalf("страница с покупкой без групп: %+v, %v", page, err)
	}
	issue := page.Issues[0]
	if issue.CheckoutID != broken || issue.TenantID != "" || issue.Reason != checkout.ReadGroup {
		t.Fatalf("признак сохранённой покупки без групп: %+v", issue)
	}
	if _, err := service.Get(ctx, buyerA, broken); !errors.Is(err, checkout.ErrUnreadable) {
		t.Fatalf("чтение сохранённой покупки без групп: %v", err)
	}
}

func TestCheckoutStuckCursorAdvancesPastInvalidDatePostgres(t *testing.T) {
	ctx := context.Background()
	pool := testPool(t)
	uow := postgres.NewUnitOfWork(pool)
	purchases := seedCheckoutList(t, uow)
	broken := purchases[3].ID()
	if _, err := pool.Exec(ctx, breakStuckCursor, string(broken)); err != nil {
		t.Fatal(err)
	}

	var got []checkout.ListEntry
	read := func(f checkout.Filter) {
		t.Helper()
		if err := do(t, uow, func(ctx context.Context, tx ports.Tx) error {
			var err error
			got, err = tx.Checkouts().List(ctx, f)
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	filter := checkout.Filter{StuckBefore: at(35), Limit: 1, OrdersOnly: true}
	read(filter)
	if len(got) != 1 || got[0].Cursor.ID != broken || !checkout.ValidStuckCursor(got[0].Cursor.StuckAt) ||
		got[0].Checkout != nil || len(got[0].Failures) != 2 || got[0].Failures[0].Reason != checkout.ReadCursor {
		t.Fatalf("повреждённый ключ: %+v", got)
	}
	filter.AfterID, filter.AfterStuckAt = got[0].Cursor.ID, got[0].Cursor.StuckAt
	read(filter)
	if len(got) != 1 || got[0].Checkout == nil || got[0].Checkout.ID() != purchases[4].ID() {
		t.Fatalf("страница после повреждённого ключа: %+v", got)
	}
}

func TestCheckoutReadCombinesHeaderAndGroupDiagnosticsPostgres(t *testing.T) {
	ctx := context.Background()
	pool := testPool(t)
	uow := postgres.NewUnitOfWork(pool)
	broken := seedCheckoutList(t, uow)[3].ID()
	for _, query := range []string{breakCheckoutTime, breakNextWork} {
		if _, err := pool.Exec(ctx, query, string(broken)); err != nil {
			t.Fatal(err)
		}
	}
	service := appcheckout.NewService(uow, nil, nil, nil, nil, fake.NewClock(at(50)), appcheckout.Config{})
	var logs bytes.Buffer
	captureCheckoutReadLogs(service, &logs)

	page, err := service.OperatorList(ctx, checkout.ListCursor{}, false)
	if err != nil || len(page.Issues) != 3 || page.Issues[0].Reason != checkout.ReadGroup ||
		page.Issues[1].Reason != checkout.ReadHeader || page.Issues[2].Reason != checkout.ReadHeader {
		t.Fatalf("причины повреждённой покупки: %+v, %v", page.Issues, err)
	}
	if lines := strings.Count(logs.String(), "\n"); lines != 1 {
		t.Fatalf("ожидалась одна строка диагностики: %s", &logs)
	}
	assertCheckoutReadLog(t, &logs, broken, "t1", "бесконечная дата покупки")
	assertCheckoutReadLog(t, &logs, broken, "t1", "бесконечный срок работы группы")
}

func TestCheckoutReadHTTPOwnerUnreadableAndForeignHealthyPostgres(t *testing.T) {
	ctx := context.Background()
	pool := testPool(t)
	uow := postgres.NewUnitOfWork(pool)
	purchases := seedCheckoutList(t, uow)
	broken := purchases[3].ID()
	if _, err := pool.Exec(ctx, breakCheckoutTotal, string(broken)); err != nil {
		t.Fatal(err)
	}
	clock := fake.NewClock(at(50))
	tokens := fake.NewIdentityTokens(clock)
	auth := appbuyer.NewAuth(uow, nil, tokens, nil, nil, clock, appbuyer.AuthConfig{})
	access, err := tokens.Open(ctx, string(buyerA))
	if err != nil {
		t.Fatal(err)
	}
	service := appcheckout.NewService(uow, nil, nil, nil, nil, clock, appcheckout.Config{})
	var logs bytes.Buffer
	captureCheckoutReadLogs(service, &logs)
	handler := transport.NewHandler(transport.Deps{
		Checkouts:              service,
		Auth:                   auth,
		RequestTimeout:         time.Second,
		ReferenceImportTimeout: time.Second,
		Registerer:             prometheus.NewRegistry(),
		Logger:                 slog.New(slog.NewTextHandler(io.Discard, nil)),
	})

	for _, tc := range []struct {
		id   ids.UUID
		code string
		want int
	}{
		{id: broken, code: checkout.ErrUnreadable.Key(), want: http.StatusInternalServerError},
		{id: purchases[4].ID(), code: checkout.ErrNotFound.Key(), want: http.StatusNotFound},
	} {
		record := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/v1/checkout/"+string(tc.id), nil)
		req.Header.Set("X-Marketplace-Money-Unit", "RUB")
		req.Header.Set("X-Request-ID", "checkout-read")
		req.AddCookie(&http.Cookie{Name: transport.SessionCookie, Value: access.Token})
		handler.ServeHTTP(record, req)
		var body struct {
			Error struct {
				Code string
			}
		}
		if err := json.Unmarshal(record.Body.Bytes(), &body); err != nil ||
			record.Code != tc.want || body.Error.Code != tc.code {
			t.Fatalf("GET покупки %s: %d %s", tc.id, record.Code, record.Body)
		}
	}
	if !strings.Contains(logs.String(), `"requestId":"checkout-read"`) {
		t.Fatalf("нет идентификатора HTTP-запроса в журнале чтения: %s", &logs)
	}
}

func TestCheckoutSellerIsolationInOperatorAndCabinetPostgres(t *testing.T) {
	const healthy ids.UUID = "a0000000-0000-0000-0000-000000000000"
	ctx := context.Background()
	pool := testPool(t)
	uow := postgres.NewUnitOfWork(pool)
	purchases := seedCheckoutList(t, uow)
	seedPickupHistoryPurchase(t, uow, "t2", pickupHistorySeed{
		id:      healthy,
		buyer:   buyerA,
		point:   "P1",
		created: at(0),
		ordered: true,
	})
	seedSellerParticipants(t, uow, "t1", "t2")
	if _, err := pool.Exec(ctx, setSellerDelivery, "t1", `{"regions":{}}`); err != nil {
		t.Fatal(err)
	}

	clock := fake.NewClock(at(50))
	tokens := fake.NewIdentityTokens(clock)
	access, err := tokens.Open(ctx, string(buyerA))
	if err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	handler := transport.NewHandler(transport.Deps{
		Checkouts:              appcheckout.NewService(uow, nil, nil, nil, nil, clock, appcheckout.Config{}),
		Auth:                   appbuyer.NewAuth(uow, nil, tokens, nil, nil, clock, appbuyer.AuthConfig{}),
		OperatorToken:          "test-operator-token",
		RequestTimeout:         time.Second,
		ReferenceImportTimeout: time.Second,
		Registerer:             prometheus.NewRegistry(),
		Logger:                 slog.New(slog.NewJSONHandler(&logs, nil)),
	})

	for _, tc := range []struct {
		path  string
		count int
	}{
		{path: "/api/v1/operator/checkouts", count: 4},
		{path: "/api/v1/orders", count: 2},
	} {
		logs.Reset()
		req := httptest.NewRequest(http.MethodGet, tc.path, nil)
		req.Header.Set("X-Marketplace-Money-Unit", "RUB")
		req.Header.Set("X-Request-ID", "seller-read")
		req.Header.Set("Authorization", "Bearer test-operator-token")
		req.AddCookie(&http.Cookie{Name: transport.SessionCookie, Value: access.Token})
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, req)
		assertCheckoutSellerPage(t, response, tc.count, purchases[3].ID(), healthy)
		errorsLogged := 0
		for _, line := range strings.Split(logs.String(), "\n") {
			if strings.Contains(line, "настройки доставки продавца") {
				errorsLogged++
				if !strings.Contains(line, `"tenantId":"t1"`) || !strings.Contains(line, `"requestId":"seller-read"`) {
					t.Fatalf("ошибка продавца без магазина или запроса: %s", line)
				}
			}
		}
		if errorsLogged != 1 {
			t.Fatalf("ошибок продавца в журнале: %d, ожидалась одна: %s", errorsLogged, &logs)
		}
	}
}

func TestOperatorMixedReadFailuresShareLogLimitPostgres(t *testing.T) {
	ctx := context.Background()
	pool := testPool(t)
	uow := postgres.NewUnitOfWork(pool)
	purchases := seedCheckoutList(t, uow)
	if _, err := pool.Exec(ctx, breakCheckoutLines, purchases[3].ID()); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, breakCheckoutTotal, purchases[4].ID()); err != nil {
		t.Fatal(err)
	}
	// Одна полностью исправная покупка и двенадцать исправных покупок с повреждённым продавцом.
	for i := range 13 {
		tenant := ids.TenantID(fmt.Sprintf("shop%d", i))
		seedSellerParticipants(t, uow, tenant)
		seedPickupHistoryPurchase(t, uow, tenant, pickupHistorySeed{
			id:      ids.UUID(fmt.Sprintf("a0000000-0000-4000-8000-%012d", i)),
			buyer:   buyerA,
			point:   "P1",
			created: at(0),
			ordered: true,
		})
		if i > 0 {
			if _, err := pool.Exec(ctx, setSellerDelivery, tenant, `{"regions":{}}`); err != nil {
				t.Fatal(err)
			}
		}
	}

	var logs bytes.Buffer
	clock := fake.NewClock(at(50))
	service := appcheckout.NewService(uow, nil, nil, nil, nil, clock, appcheckout.Config{})
	captureCheckoutReadLogs(service, &logs)
	handler := transport.NewHandler(transport.Deps{
		Checkouts:              service,
		OperatorToken:          "test-operator-token",
		RequestTimeout:         time.Minute,
		ReferenceImportTimeout: time.Minute,
		Registerer:             prometheus.NewRegistry(),
		Logger:                 slog.New(slog.NewJSONHandler(&logs, nil)),
	})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/operator/checkouts", nil)
	req.Header.Set("X-Marketplace-Money-Unit", "RUB")
	req.Header.Set("X-Request-ID", "mixed-page")
	req.Header.Set("Authorization", "Bearer test-operator-token")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, req)

	var page struct {
		Items []struct {
			ID     ids.UUID
			Groups []struct {
				Seller   *participant.Seller
				TenantID ids.TenantID
			}
		}
		Issues []appcheckout.ReadIssue
	}
	if err := json.Unmarshal(response.Body.Bytes(), &page); err != nil || response.Code != http.StatusOK {
		t.Fatalf("страница оператора: %d %s, %v", response.Code, response.Body, err)
	}
	if len(page.Items) != 14 || len(page.Issues) != 3 {
		t.Fatalf("покупки и ошибки: %d, %+v", len(page.Items), page.Issues)
	}
	reasons := map[ids.UUID]string{}
	for _, issue := range page.Issues {
		reasons[issue.CheckoutID] = issue.Reason
	}
	if reasons[purchases[3].ID()] != checkout.ReadGroup || reasons[purchases[4].ID()] != checkout.ReadRender {
		t.Fatalf("причины пропуска покупок: %+v", reasons)
	}
	sellers := map[ids.TenantID]*participant.Seller{}
	for _, item := range page.Items {
		for _, group := range item.Groups {
			sellers[group.TenantID] = group.Seller
		}
	}
	if seller := sellers["shop0"]; seller == nil || seller.Slug != "shop-shop0" {
		t.Fatalf("потерян исправный продавец: %+v", seller)
	}
	for i := 1; i <= 12; i++ {
		if seller, found := sellers[ids.TenantID(fmt.Sprintf("shop%d", i))]; !found || seller != nil {
			t.Fatalf("покупка повреждённого продавца %d: %+v, найдена %t", i, seller, found)
		}
	}

	var total, sellerErrors int
	for _, line := range strings.Split(logs.String(), "\n") {
		if !strings.Contains(line, `"level":"ERROR"`) {
			continue
		}
		total++
		if !strings.Contains(line, `"requestId":"mixed-page"`) {
			t.Fatal(line)
		}
		if strings.Contains(line, "настройки доставки продавца") {
			sellerErrors++
		}
	}
	if total != 10 || sellerErrors != 8 {
		t.Fatalf("журнал смешанной страницы: %d ошибок, %d у продавцов: %s", total, sellerErrors, &logs)
	}
}

func assertCheckoutSellerPage(t *testing.T, response *httptest.ResponseRecorder, count int, mixed, healthy ids.UUID) {
	t.Helper()
	var body struct {
		Items []struct {
			ID     ids.UUID
			Groups []struct {
				Seller   *participant.Seller
				TenantID ids.TenantID
			}
		}
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil || response.Code != http.StatusOK ||
		len(body.Items) != count {
		t.Fatalf("список покупок: %d %s (%v)", response.Code, response.Body, err)
	}

	found := map[ids.UUID]bool{}
	for _, item := range body.Items {
		if item.ID != mixed && item.ID != healthy {
			continue
		}
		found[item.ID] = true
		for _, group := range item.Groups {
			if group.TenantID == "t1" && item.ID == mixed && group.Seller == nil {
				continue
			}
			if group.TenantID != "t2" || group.Seller == nil || group.Seller.Slug != "shop-t2" {
				t.Fatalf("продавец %s покупки %s: %+v", group.TenantID, item.ID, group.Seller)
			}
		}
		if (item.ID == healthy && len(item.Groups) != 1) || (item.ID == mixed && len(item.Groups) != 2) {
			t.Fatalf("группы покупки %s: %+v", item.ID, item.Groups)
		}
	}
	if !found[mixed] || !found[healthy] || !strings.Contains(response.Body.String(), `"seller":null`) {
		t.Fatalf("нет покупок или null у повреждённого продавца: %s", response.Body)
	}
}

func captureCheckoutReadLogs(service *appcheckout.Service, logs *bytes.Buffer) *prometheus.Registry {
	logger := slog.New(slog.NewJSONHandler(logs, nil))
	registry := prometheus.NewRegistry()
	service.SetObserver(jobs.NewCheckoutMetrics(logger, registry))
	return registry
}

func assertCheckoutReadLog(t *testing.T, logs *bytes.Buffer, id ids.UUID, tenant ids.TenantID, cause string) {
	t.Helper()
	for _, line := range strings.Split(logs.String(), "\n") {
		if strings.Contains(line, `"checkoutId":"`+string(id)+`"`) &&
			strings.Contains(line, `"tenantId":"`+string(tenant)+`"`) && strings.Contains(line, cause) {
			return
		}
	}
	t.Fatalf("нет причины %q для покупки %s магазина %s: %s", cause, id, tenant, logs)
}
