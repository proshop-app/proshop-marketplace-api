//go:build integration

package postgres_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/checkout"
	"github.com/proshop-app/proshop-marketplace/internal/domain/delivery"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/postgres"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
	"github.com/proshop-app/proshop-marketplace/pkg/money"
)

const (
	checkoutListPlanSettings = `
		SET LOCAL enable_seqscan = off;
		SET LOCAL enable_sort = off;
		SET LOCAL plan_cache_mode = force_generic_plan`
	prepareCheckoutListPlan    = `PREPARE checkout_list_plan AS `
	explainCheckoutListPlan    = `EXPLAIN (COSTS OFF) EXECUTE checkout_list_plan($1, $2, $3, $4, $5, $6)`
	deallocateCheckoutListPlan = `DEALLOCATE checkout_list_plan`
)

type checkoutListTrace struct {
	queries      int
	listSQL      string
	listArgs     []any
	beforeGroups func()
}

func (trace *checkoutListTrace) TraceQueryStart(
	ctx context.Context,
	_ *pgx.Conn,
	data pgx.TraceQueryStartData,
) context.Context {
	if strings.HasPrefix(strings.TrimSpace(data.SQL), "SELECT") {
		trace.queries++
	}
	if strings.Contains(data.SQL, "FROM checkouts c") {
		trace.listSQL, trace.listArgs = data.SQL, data.Args
	}
	if strings.Contains(data.SQL, "FROM unnest($1::uuid[], $2::bigint[])") {
		if trace.beforeGroups != nil {
			hook := trace.beforeGroups
			trace.beforeGroups = nil
			hook()
		}
	}
	return ctx
}

func (*checkoutListTrace) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

func TestCheckoutListPaginationFiltersAndBatchLoading(t *testing.T) {
	pool := testPool(t)
	uow := postgres.NewUnitOfWork(pool)
	purchases := seedCheckoutList(t, uow)
	trace := &checkoutListTrace{}
	traced := checkoutListUnitOfWork(t, pool, trace)
	allIDs := make([]ids.UUID, len(purchases))
	for i, purchase := range purchases {
		allIDs[i] = purchase.ID()
	}

	filter := checkout.Filter{Limit: 2}
	var paged []ids.UUID
	for {
		items := checkoutListRead(t, traced, trace, filter)
		if len(items) == 0 {
			break
		}
		for _, item := range items {
			paged = append(paged, item.ID())
		}
		filter.AfterID = items[len(items)-1].ID()
	}
	if !slices.Equal(paged, allIDs) {
		t.Fatalf("порядок страниц: %v, ожидалось %v", paged, allIDs)
	}

	for _, tc := range []struct {
		name   string
		filter checkout.Filter
		want   []ids.UUID
	}{
		{
			name:   "все покупки",
			filter: checkout.Filter{Limit: 100},
			want:   allIDs,
		},
		{
			name:   "покупатель",
			filter: checkout.Filter{BuyerID: buyerA, Limit: 100},
			want:   []ids.UUID{allIDs[0], allIDs[2], allIDs[3]},
		},
		{
			name:   "заказы",
			filter: checkout.Filter{OrdersOnly: true, Limit: 100},
			want:   allIDs[2:],
		},
		{
			name:   "заказы покупателя после курсора",
			filter: checkout.Filter{BuyerID: buyerA, AfterID: allIDs[0], OrdersOnly: true, Limit: 1},
			want:   []ids.UUID{allIDs[2]},
		},
		{
			name:   "давно ожидают оплаты",
			filter: checkout.Filter{OrdersOnly: true, AwaitingBefore: at(15), Limit: 100},
			want:   []ids.UUID{allIDs[3]},
		},
		{
			name:   "все ожидающие",
			filter: checkout.Filter{AwaitingBefore: at(35), Limit: 100},
			want:   allIDs[3:],
		},
		{
			name:   "аренда создания ещё действует",
			filter: checkout.Filter{OrdersOnly: true, WorkDueAt: at(0), Limit: 100},
		},
		{
			name:   "пора создавать",
			filter: checkout.Filter{OrdersOnly: true, WorkDueAt: at(1), Limit: 100},
			want:   []ids.UUID{allIDs[2]},
		},
		{
			name:   "пора создавать и опрашивать",
			filter: checkout.Filter{OrdersOnly: true, WorkDueAt: at(10), Limit: 100},
			want:   allIDs[2:4],
		},
		{
			name:   "все задания готовы",
			filter: checkout.Filter{OrdersOnly: true, WorkDueAt: at(30), Limit: 100},
			want:   allIDs[2:],
		},
		{
			name: "ожидающий заказ ещё рано опрашивать",
			filter: checkout.Filter{
				OrdersOnly:     true,
				AwaitingBefore: at(15),
				WorkDueAt:      at(9),
				Limit:          100,
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			items := checkoutListRead(t, traced, trace, tc.filter)
			got := make([]ids.UUID, 0, len(items))
			for _, item := range items {
				got = append(got, item.ID())
				index := slices.Index(allIDs, item.ID())
				assertCheckoutListState(t, item.State(), purchases[index].State())
			}
			if !slices.Equal(got, tc.want) {
				t.Fatalf("покупки: %v, ожидалось %v", got, tc.want)
			}
		})
	}

	for _, invalid := range []checkout.Filter{
		{BuyerID: "invalid", Limit: 100},
		{AfterID: "invalid", Limit: 100},
	} {
		trace.queries = 0
		err := do(t, traced, func(ctx context.Context, tx ports.Tx) error {
			_, err := tx.Checkouts().List(ctx, invalid)
			return err
		})
		if !errors.Is(err, ids.ErrInvalid) || trace.queries != 0 {
			t.Fatalf("некорректный UUID: %v, запросов: %d", err, trace.queries)
		}
	}

	for _, tc := range []struct {
		buyer      ids.UUID
		after      ids.UUID
		index      string
		conditions []string
	}{
		{index: "checkouts_pkey"},
		{
			after:      allIDs[0],
			index:      "checkouts_pkey",
			conditions: []string{"id > $2"},
		},
		{
			buyer:      buyerA,
			index:      "checkouts_buyer_cursor_idx",
			conditions: []string{"buyer_id = $1"},
		},
		{
			buyer:      buyerA,
			after:      allIDs[0],
			index:      "checkouts_buyer_cursor_idx",
			conditions: []string{"buyer_id = $1", "id > $2"},
		},
	} {
		checkoutListRead(t, traced, trace, checkout.Filter{BuyerID: tc.buyer, AfterID: tc.after, Limit: 100})
		assertCheckoutListIndex(t, pool, trace, tc.index, tc.conditions)
	}
}

func TestCheckoutListRejectsChangedVersionBetweenQueries(t *testing.T) {
	pool := testPool(t)
	uow := postgres.NewUnitOfWork(pool)
	purchases := seedCheckoutList(t, uow)
	trace := &checkoutListTrace{beforeGroups: func() {
		purchase := purchases[3]
		if _, err := purchase.ClaimPoll("t1", "concurrent-poll", at(10)); err != nil {
			t.Fatal(err)
		}
		if err := do(t, uow, func(ctx context.Context, tx ports.Tx) error {
			return tx.Checkouts().Update(ctx, purchase)
		}); err != nil {
			t.Fatal(err)
		}
	}}
	traced := checkoutListUnitOfWork(t, pool, trace)
	err := do(t, traced, func(ctx context.Context, tx ports.Tx) error {
		_, err := tx.Checkouts().List(ctx, checkout.Filter{Limit: 100})
		return err
	})
	if !errors.Is(err, checkout.ErrConflict) {
		t.Fatalf("покупка и группы разных версий: %v", err)
	}
	if trace.queries != 2 {
		t.Fatalf("запросов: %d, ожидалось 2", trace.queries)
	}
}

func checkoutListUnitOfWork(t *testing.T, pool *pgxpool.Pool, trace *checkoutListTrace) *postgres.UnitOfWork {
	t.Helper()
	config := pool.Config()
	config.ConnConfig.Tracer = trace
	tracedPool, err := pgxpool.NewWithConfig(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(tracedPool.Close)
	return postgres.NewUnitOfWork(tracedPool)
}

func checkoutListRead(
	t *testing.T,
	uow *postgres.UnitOfWork,
	trace *checkoutListTrace,
	filter checkout.Filter,
) []*checkout.Checkout {
	t.Helper()
	trace.queries = 0
	var items []*checkout.Checkout
	err := do(t, uow, func(ctx context.Context, tx ports.Tx) error {
		var err error
		items, err = tx.Checkouts().List(ctx, filter)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	wantQueries := 2
	if len(items) == 0 {
		wantQueries = 1
	}
	if trace.queries != wantQueries {
		t.Fatalf("запросов: %d, ожидалось %d для %d покупок", trace.queries, wantQueries, len(items))
	}
	return items
}

func assertCheckoutListState(t *testing.T, got, want checkout.State) {
	t.Helper()
	// timestamptz возвращается в часовом поясе соединения; сравниваем сами моменты времени.
	got.CreatedAt = got.CreatedAt.UTC()
	got.UpdatedAt = got.UpdatedAt.UTC()
	got.ConfirmedAt = got.ConfirmedAt.UTC()
	for i := range got.Groups {
		if quote := got.Groups[i].Quote; quote != nil {
			quote.ExpiresAt = quote.ExpiresAt.UTC()
		}
	}

	gotJSON, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	wantJSON, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	if string(gotJSON) != string(wantJSON) {
		t.Fatalf("снимок покупки после пакетного чтения:\n%s\nожидалось:\n%s", gotJSON, wantJSON)
	}
}

func assertCheckoutListIndex(
	t *testing.T,
	pool *pgxpool.Pool,
	trace *checkoutListTrace,
	index string,
	conditions []string,
) {
	t.Helper()
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, checkoutListPlanSettings); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, prepareCheckoutListPlan+trace.listSQL); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = tx.Exec(ctx, deallocateCheckoutListPlan) }()

	// Simple protocol подставляет экранированные параметры EXECUTE; план строит сам PREPARE.
	args := append([]any{pgx.QueryExecModeSimpleProtocol}, trace.listArgs...)
	rows, err := tx.Query(ctx, explainCheckoutListPlan, args...)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var plan strings.Builder
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			t.Fatal(err)
		}
		plan.WriteString(line + "\n")
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(plan.String(), index) || strings.Contains(plan.String(), "Sort") {
		t.Fatalf("ожидался курсор по индексу %s без сортировки:\n%s", index, plan.String())
	}
	for _, condition := range conditions {
		found := false
		for _, line := range strings.Split(plan.String(), "\n") {
			if strings.Contains(line, "Index Cond:") && strings.Contains(line, condition) {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("generic plan потерял индексное условие %s:\n%s", condition, plan.String())
		}
	}
}

func seedCheckoutList(t *testing.T, uow *postgres.UnitOfWork) []*checkout.Checkout {
	t.Helper()
	seedBuyer(t, uow, buyerA, "79251234567")
	seedBuyer(t, uow, buyerB, "79251234568")
	var purchases []*checkout.Checkout
	for i, value := range []struct {
		id     ids.UUID
		buyer  ids.UUID
		status checkout.Status
		at     time.Time
	}{
		{id: "00000000-0000-0000-0000-000000000000", buyer: buyerA, status: checkout.StatusDraft},
		{id: "10000000-0000-0000-0000-000000000000", buyer: buyerB, status: checkout.StatusConfirmed},
		{id: "20000000-0000-0000-0000-000000000000", buyer: buyerA, status: checkout.StatusCreatingOrders},
		{
			id:     "90000000-0000-0000-0000-000000000000",
			buyer:  buyerA,
			status: checkout.StatusAwaitingPayment,
			at:     at(10),
		},
		{
			id:     "ffffffff-ffff-ffff-ffff-ffffffffffff",
			buyer:  buyerB,
			status: checkout.StatusAwaitingPayment,
			at:     at(30),
		},
	} {
		purchase, err := checkout.New(
			value.id,
			value.buyer,
			fmt.Sprintf("list-key-%d", i),
			[]checkout.Line{
				{
					Offer:    ids.OfferID{Tenant: "t2", Product: "p2"},
					Name:     "Товар 2",
					Quantity: 1,
					Price:    money.FromKopecks(200),
				},
				{
					Offer:    ids.OfferID{Tenant: "t1", Product: "p1"},
					Name:     "Товар 1",
					Quantity: 2,
					Price:    money.FromKopecks(100),
				},
			},
			at(0),
		)
		if err != nil {
			t.Fatal(err)
		}
		if value.status != checkout.StatusDraft {
			if err := purchase.SelectDelivery(
				checkout.Selection{Carrier: "cdek", PickupPointCode: "P1"},
				map[ids.TenantID]delivery.Quote{
					"t1": {Status: delivery.QuoteAvailable, ExpiresAt: at(60), BuyerCost: money.FromKopecks(10)},
					"t2": {Status: delivery.QuoteAvailable, ExpiresAt: at(60), BuyerCost: money.FromKopecks(20)},
				},
				at(0),
			); err != nil {
				t.Fatal(err)
			}
			if err := purchase.Confirm(at(0)); err != nil {
				t.Fatal(err)
			}
		}
		if value.status != checkout.StatusDraft && value.status != checkout.StatusConfirmed {
			contact := checkout.Contact{Phone: "79251234567", Name: "Покупатель"}
			if _, err := purchase.BeginOrders(contact, at(0)); err != nil {
				t.Fatal(err)
			}
			if _, err := purchase.MarkCartConsumed(at(0)); err != nil {
				t.Fatal(err)
			}
			for _, group := range purchase.Groups() {
				if value.status == checkout.StatusCreatingOrders {
					if _, err := purchase.ClaimCreation(group.TenantID, "in-progress", at(0)); err != nil {
						t.Fatal(err)
					}
					continue
				}
				total, err := group.Goods.Add(group.DeliveryPrice)
				if err != nil {
					t.Fatal(err)
				}
				order := checkout.SellerOrder{
					OrderID:   "o-" + string(group.TenantID),
					ExpiresAt: at(60),
					Total:     total,
				}
				if _, err := purchase.AttachOrder(group.TenantID, order, value.at); err != nil {
					t.Fatal(err)
				}
			}
		}
		purchases = append(purchases, purchase)
	}
	for _, purchase := range slices.Backward(purchases) {
		if err := do(t, uow, func(ctx context.Context, tx ports.Tx) error {
			return tx.Checkouts().Add(ctx, purchase)
		}); err != nil {
			t.Fatal(err)
		}
	}
	return purchases
}
