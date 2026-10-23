//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/postgres"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
	"github.com/proshop-app/proshop-marketplace/pkg/money"
)

func enqueue(t *testing.T, uow *postgres.UnitOfWork, tenant ids.TenantID, key string, minute int) {
	t.Helper()

	err := uow.Do(context.Background(), func(ctx context.Context, tx ports.Tx) error {
		return tx.Outbox().Enqueue(ctx, ports.NewTask{
			Kind:     "test",
			TenantID: tenant,
			DedupKey: key,
			Payload:  []byte(`{"tenantId":"` + string(tenant) + `"}`),
			At:       at(minute),
		})
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestOutboxDedupClaimLease(t *testing.T) {
	pool := testPool(t)
	uow := postgres.NewUnitOfWork(pool)
	queue := postgres.NewTaskQueue(pool)
	ctx := context.Background()

	enqueue(t, uow, "t1", "k1", 0)
	enqueue(t, uow, "t1", "k1", 1)

	tasks, err := queue.Claim(ctx, ports.ClaimRequest{Now: at(5), Limit: 10, PerTenant: 10, LeaseUntil: at(6)})
	if err != nil || len(tasks) != 1 || tasks[0].Attempts != 1 || string(tasks[0].Payload) != `{"tenantId": "t1"}` {
		t.Fatalf("невзятые задачи с одним ключом склеиваются: %+v, %v", tasks, err)
	}

	enqueue(t, uow, "t1", "k1", 5)

	again, err := queue.Claim(ctx, ports.ClaimRequest{Now: at(5), Limit: 10, PerTenant: 10, LeaseUntil: at(6)})
	if err != nil || len(again) != 1 || again[0].ID == tasks[0].ID {
		t.Fatalf("после взятия новое изменение ставит новую задачу, взятая — в аренде: %+v, %v", again, err)
	}

	if err := queue.Complete(ctx, again[0].ID, at(5)); err != nil {
		t.Fatal(err)
	}

	expired, err := queue.Claim(ctx, ports.ClaimRequest{Now: at(7), Limit: 10, PerTenant: 10, LeaseUntil: at(8)})
	if err != nil || len(expired) != 1 || expired[0].ID != tasks[0].ID || expired[0].Attempts != 2 {
		t.Fatalf("после аренды задачу берёт другой обработчик: %+v, %v", expired, err)
	}

	if err := queue.Retry(ctx, expired[0].ID, at(30), "сбой"); err != nil {
		t.Fatal(err)
	}

	early := ports.ClaimRequest{Now: at(20), Limit: 10, PerTenant: 10, LeaseUntil: at(21)}
	if none, _ := queue.Claim(ctx, early); len(none) != 0 {
		t.Fatalf("отложенная задача не созрела: %+v", none)
	}

	var lastError string

	err = pool.QueryRow(ctx, `SELECT last_error FROM outbox WHERE id = $1`, expired[0].ID).Scan(&lastError)
	if err != nil || lastError != "сбой" {
		t.Fatalf("причина повтора: %q, %v", lastError, err)
	}
}

func TestOutboxClaimIsFairAcrossTenants(t *testing.T) {
	pool := testPool(t)
	uow := postgres.NewUnitOfWork(pool)
	queue := postgres.NewTaskQueue(pool)
	ctx := context.Background()

	for i := range 10 {
		enqueue(t, uow, "noisy", fmt.Sprintf("n%d", i), 0)
	}
	enqueue(t, uow, "quiet", "q", 1)

	tasks, err := queue.Claim(ctx, ports.ClaimRequest{Now: at(5), Limit: 5, PerTenant: 3, LeaseUntil: at(6)})
	if err != nil {
		t.Fatal(err)
	}

	perTenant := map[ids.TenantID]int{}
	for _, task := range tasks {
		perTenant[task.TenantID]++
	}
	if perTenant["noisy"] != 3 || perTenant["quiet"] != 1 {
		t.Fatalf("шумный магазин не забирает всю пачку: %+v", perTenant)
	}
}

func TestOutboxConcurrentClaimsDoNotOverlap(t *testing.T) {
	pool := testPool(t)
	uow := postgres.NewUnitOfWork(pool)
	queue := postgres.NewTaskQueue(pool)
	ctx := context.Background()

	for i := range 40 {
		enqueue(t, uow, ids.TenantID(fmt.Sprintf("t%d", i%4)), fmt.Sprintf("k%d", i), 0)
	}

	var (
		mu   sync.Mutex
		seen = map[int64]int{}
		wg   sync.WaitGroup
	)

	for range 4 {
		wg.Add(1)

		go func() {
			defer wg.Done()

			for {
				tasks, err := queue.Claim(
					ctx,
					ports.ClaimRequest{Now: at(5), Limit: 3, PerTenant: 2, LeaseUntil: at(30)},
				)
				if err != nil {
					t.Error(err)

					return
				}
				if len(tasks) == 0 {
					return
				}

				mu.Lock()
				for _, task := range tasks {
					seen[task.ID]++
				}
				mu.Unlock()
			}
		}()
	}

	wg.Wait()

	if len(seen) != 40 {
		t.Fatalf("взяты все задачи: %d", len(seen))
	}
	for id, count := range seen {
		if count != 1 {
			t.Fatalf("задачу %d взяли %d раз", id, count)
		}
	}
}

func TestInboxParkedRetention(t *testing.T) {
	pool := testPool(t)
	uow := postgres.NewUnitOfWork(pool)
	queue := postgres.NewTaskQueue(pool)
	ctx := context.Background()

	err := uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		fresh, err := tx.Inbox().MarkProcessed(ctx, "topic:0:1", at(0))
		if err != nil || !fresh {
			t.Fatalf("первая отметка: %v, %v", fresh, err)
		}
		fresh, err = tx.Inbox().MarkProcessed(ctx, "topic:0:1", at(1))
		if err != nil || fresh {
			t.Fatalf("повтор: %v, %v", fresh, err)
		}
		if _, err := tx.Inbox().MarkProcessed(ctx, "topic:0:2", at(50)); err != nil {
			return err
		}

		event := ports.ParkedEvent{
			Topic:     "topic",
			Partition: 0,
			Offset:    3,
			Key:       []byte("k"),
			Value:     []byte("{"),
			Headers: map[string]string{
				"x-tenant": "t1",
			},
			TenantID: "t1",
			Reason:   "invalid_payload",
			Error:    "json",
			ParkedAt: at(0),
		}
		if err := tx.Parked().Park(ctx, event); err != nil {
			return err
		}

		return tx.Parked().Park(ctx, event)
	})
	if err != nil {
		t.Fatal(err)
	}

	var parked int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM parked_events`).Scan(&parked); err != nil || parked != 1 {
		t.Fatalf("повтор парковки той же позиции: %d, %v", parked, err)
	}

	enqueue(t, uow, "t1", "a", 0)
	tasks, _ := queue.Claim(ctx, ports.ClaimRequest{Now: at(1), Limit: 1, PerTenant: 1, LeaseUntil: at(2)})
	if err := queue.Complete(ctx, tasks[0].ID, at(1)); err != nil {
		t.Fatal(err)
	}
	enqueue(t, uow, "t1", "b", 0)

	result, err := postgres.NewRetention(pool).Purge(ctx, ports.PurgeRequest{
		ProcessedBefore:  at(10),
		OutboxDoneBefore: at(10),
		ParkedBefore:     at(10),
		ChallengesBefore: at(10),
		CartsBefore:      at(10),
		Batch:            1,
	})
	if err != nil || result.Processed != 1 || result.OutboxDone != 1 || result.Parked != 1 {
		t.Fatalf("чистка пачками: %+v, %v", result, err)
	}

	var pending int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM outbox WHERE done_at IS NULL`).Scan(&pending); err != nil ||
		pending != 1 {
		t.Fatalf("невыполненные задачи не чистятся: %d, %v", pending, err)
	}
}

func TestOfferStates(t *testing.T) {
	pool := testPool(t)
	uow := postgres.NewUnitOfWork(pool)
	ctx := context.Background()
	seedTenant(t, uow, "t1", "shop-one", participant.TenantActive)
	seedTenant(t, uow, "t2", "shop-two", participant.TenantActive)

	err := uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		tenant, _ := tx.Registry().Get(ctx, "t1")
		p, _ := participant.New(tenant, "Первый", at(0))
		p.Enable(at(0))
		if err := tx.Participants().Add(ctx, p); err != nil {
			return err
		}

		for _, key := range []struct {
			tenant  ids.TenantID
			product ids.ProductID
		}{{"t1", "p1"}, {"t1", "p2"}, {"t1", "p3"}, {"t2", "p1"}} {
			product, _ := catalog.NewProduct(snapshot(key.tenant, key.product, 1, 0), at(0))
			if err := tx.Products().Save(ctx, product); err != nil {
				return err
			}
		}

		signals := catalog.NewSignals("t1", "p2")
		_, _ = signals.ApplyPrice(catalog.PriceSignal{Price: money.FromKopecks(5000), At: at(30)}, at(30))

		return tx.Signals().Save(ctx, signals)
	})
	if err != nil {
		t.Fatal(err)
	}

	_ = uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		state, err := tx.Offers().Offer(ctx, "t1", "p2")
		if err != nil || state.Participant == nil || state.Tenant == nil || state.Signals == nil ||
			state.Signals.State().Price.Kopecks() != 5000 || state.Tenant.Status() != participant.TenantActive {
			t.Fatalf("снимок предложения: %+v, %v", state, err)
		}

		outsider, err := tx.Offers().Offer(ctx, "t2", "p1")
		if err != nil || outsider.Participant != nil || outsider.Signals != nil || outsider.Tenant == nil {
			t.Fatalf("товар магазина без участия: %+v, %v", outsider, err)
		}

		if _, err := tx.Offers().Offer(ctx, "t1", "missing"); !errors.Is(err, catalog.ErrProductNotFound) {
			t.Fatalf("ожидалось ErrProductNotFound, получено %v", err)
		}

		page, err := tx.Offers().TenantOffers(ctx, "t1", "p1", 1)
		if err != nil || len(page) != 1 || page[0].Product.State().ProductID != "p2" {
			t.Fatalf("страница магазина: %+v, %v", page, err)
		}

		all, err := tx.Offers().ParticipantOffers(ctx, ports.OfferKey{}, 10)
		if err != nil || len(all) != 3 {
			t.Fatalf("предложения участников: %d, %v", len(all), err)
		}

		next, err := tx.Offers().ParticipantOffers(ctx, ports.OfferKey{TenantID: "t1", ProductID: "p2"}, 10)
		if err != nil || len(next) != 1 || next[0].Product.State().ProductID != "p3" {
			t.Fatalf("продолжение после позиции: %+v, %v", next, err)
		}

		changed, err := tx.Offers().ChangedSince(ctx, at(10), ports.OfferKey{}, 10)
		if err != nil || len(changed) != 1 || changed[0].Product.State().ProductID != "p2" {
			t.Fatalf("изменённые после границы: %+v, %v", changed, err)
		}

		return nil
	})
}
