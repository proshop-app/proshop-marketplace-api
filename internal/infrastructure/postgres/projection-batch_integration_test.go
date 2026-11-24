//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	appcatalog "github.com/proshop-app/proshop-marketplace/internal/application/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/postgres"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

const (
	batchProductsState = `SELECT count(*), min(revision), max(revision), min(version), max(version)
        FROM catalog_products WHERE product_id LIKE 'x%'`
	batchVerdictsState = `SELECT count(*), count(*) FILTER (WHERE jsonb_typeof(decision) = 'null')
        FROM moderation_verdicts WHERE product_id LIKE 'x%'`
	batchOutboxState  = `SELECT count(*) FROM outbox WHERE dedup_key LIKE '%~x%'`
	bumpBatchProduct  = `UPDATE catalog_products SET version = version + 10 WHERE product_id = 'xp7'`
	batchProductCount = 20
)

// Пачка снимков пишет товары, вердикты и задачи одним оператором на таблицу; конфликт версии
// любого товара откатывает всю пачку.
func TestProjectionBatchWritesAndRollsBackTogether(t *testing.T) {
	ctx := context.Background()
	pool := testPool(t)
	uow := postgres.NewUnitOfWork(pool)
	projector := seedProjectionIntake(ctx, t, uow, pool, 0)
	batch := func(revision int64) []appcatalog.ProductEvent {
		events := make([]appcatalog.ProductEvent, 0, batchProductCount)
		for n := range batchProductCount {
			events = append(events, intakeScaleEvent(n, revision, "x"))
		}
		return events
	}
	if err := projector.ApplyProducts(ctx, batch(1)); err != nil {
		t.Fatal(err)
	}
	assertBatchProducts(t, pool, [5]int64{batchProductCount, 1, 1, 1, 1})
	var verdicts, nullDecisions, tasks int64
	if err := pool.QueryRow(ctx, batchVerdictsState).Scan(&verdicts, &nullDecisions); err != nil {
		t.Fatal(err)
	}
	// Пустое решение пачкой — jsonb null, как у одиночного сохранения.
	if verdicts != batchProductCount || nullDecisions != batchProductCount {
		t.Fatal("вердикты пачки", verdicts, nullDecisions)
	}
	if err := pool.QueryRow(ctx, batchOutboxState).Scan(&tasks); err != nil || tasks != 2*batchProductCount {
		t.Fatal("задачи пачки", tasks, err)
	}
	if err := projector.ApplyProducts(ctx, batch(2)); err != nil {
		t.Fatal(err)
	}
	assertBatchProducts(t, pool, [5]int64{batchProductCount, 2, 2, 2, 2})
	err := uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		products, err := tx.Products().GetMany(ctx, batchKeys())
		if err != nil {
			return err
		}
		// Соседний процесс меняет товар между чтением и записью пачки.
		if _, err := pool.Exec(ctx, bumpBatchProduct); err != nil {
			return err
		}
		changed := make([]*catalog.Product, 0, len(products))
		for _, product := range products {
			snapshot := intakeScaleEvent(0, 3, "x").Snapshot
			snapshot.ProductID = product.State().ProductID
			snapshot.TenantID = product.State().TenantID
			if _, err := product.Apply(snapshot, time.Now()); err != nil {
				return err
			}
			changed = append(changed, product)
		}
		return tx.Products().SaveMany(ctx, changed)
	})
	if !errors.Is(err, catalog.ErrConflict) {
		t.Fatal("конфликт версии одного товара должен откатить пачку", err)
	}
	assertBatchProducts(t, pool, [5]int64{batchProductCount, 2, 2, 2, 12})
}

func batchKeys() []ids.OfferID {
	keys := make([]ids.OfferID, 0, batchProductCount)
	for n := range batchProductCount {
		snapshot := intakeScaleEvent(n, 1, "x").Snapshot
		keys = append(keys, ids.OfferID{Tenant: snapshot.TenantID, Product: snapshot.ProductID})
	}
	return keys
}

// assertBatchProducts сверяет число товаров пачки, границы ревизий и версий.
func assertBatchProducts(t *testing.T, pool *pgxpool.Pool, want [5]int64) {
	t.Helper()
	var got [5]int64
	row := pool.QueryRow(context.Background(), batchProductsState)
	if err := row.Scan(&got[0], &got[1], &got[2], &got[3], &got[4]); err != nil || got != want {
		t.Fatal("товары пачки", got, want)
	}
}
