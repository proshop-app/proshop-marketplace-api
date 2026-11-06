//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	app "github.com/proshop-app/proshop-marketplace/internal/application/participant"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/postgres"
)

func seedStoredCard(t *testing.T, uow *postgres.UnitOfWork) ports.StoreRecord {
	t.Helper()
	ctx := context.Background()
	seedTenant(t, uow, "t1", "shop", participant.TenantActive)
	record := ports.StoreRecord{
		ProfileVersion: 1,
		Summary: ports.StoreSummary{
			Offers:     1,
			Categories: []ports.StoreCategory{},
			Showcase:   []ports.StoreProduct{},
			Hidden:     []participant.CardHidden{},
		},
		Header: ports.ShopHeader{TenantID: "t1", Slug: "shop", Name: "Магазин", Slogan: "Слоган", Offers: 1},
		Card:   ports.ShopCard{TenantID: "t1", Slug: "shop", Name: "Магазин", Slogan: "Слоган", Offers: 1},
		Public: true,
	}
	if err := uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		tenant, err := tx.Registry().Get(ctx, "t1")
		if err != nil {
			return err
		}
		p, err := participant.New(tenant, "Магазин", at(0))
		if err != nil {
			return err
		}
		if _, err := p.ApplyStoreCard(
			participant.Profile{Name: "Магазин", Slogan: "Слоган"},
			participant.CardReadiness{Ready: true},
			nil,
			at(0),
		); err != nil {
			return err
		}
		if err := tx.Participants().Add(ctx, p); err != nil {
			return err
		}
		record.ProfileVersion = p.Version()
		return tx.StoreCards().Save(ctx, record)
	}); err != nil {
		t.Fatal(err)
	}
	return record
}

func TestStoreRefreshLockDoesNotBlockModeration(t *testing.T) {
	uow := postgres.NewUnitOfWork(testPool(t))
	stale := seedStoredCard(t, uow)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	locked, release, finished := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		finished <- uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
			if err := tx.StoreCards().Lock(ctx, "t1"); err != nil {
				return err
			}
			close(locked)
			select {
			case <-release:
				return tx.StoreCards().Save(ctx, stale)
			case <-ctx.Done():
				return ctx.Err()
			}
		})
	}()
	select {
	case <-locked:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	// Другой пересчёт ждёт advisory lock, решение оператора — нет.
	waitCtx, stop := context.WithTimeout(ctx, 100*time.Millisecond)
	err := uow.Do(waitCtx, func(ctx context.Context, tx ports.Tx) error {
		return tx.StoreCards().Lock(ctx, "t1")
	})
	stop()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("пересчёты не сериализованы: %v", err)
	}
	service := app.NewService(uow, fake.NewClock(at(1)), &fake.IDs{})
	decision := participant.Decision{Note: "Причина"}
	if _, err := service.ModerateCard(ctx, "t1", participant.CardSlogan, true, decision); err != nil {
		t.Fatal(err)
	}
	header, err := service.Shop(ctx, "shop")
	if err != nil || header.Slogan != "" {
		t.Fatal(header, err)
	}
	close(release)
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
	header, err = service.Shop(ctx, "shop")
	if err != nil || header.Slogan != "" {
		t.Fatalf("старый пересчёт вернул слоган: %+v %v", header, err)
	}
	summary, err := service.StoreSummary(ctx, "t1")
	if err != nil || len(summary.Hidden) != 1 {
		t.Fatal(summary, err)
	}
	if _, err := service.ModerateCard(ctx, "t1", participant.CardSlogan, false, participant.Decision{}); err != nil {
		t.Fatal(err)
	}
	page, err := service.Shops(ctx, "", 20, 0)
	if err != nil || len(page.Items) != 1 || page.Items[0].Slogan != "Слоган" {
		t.Fatal(page, err)
	}
}

func TestStoreRefreshDedupKeepsEarliestTime(t *testing.T) {
	pool := testPool(t)
	uow := postgres.NewUnitOfWork(pool)
	ctx := context.Background()
	now := at(0)
	for _, delay := range []time.Duration{30 * time.Second, time.Minute, 0, 45 * time.Second} {
		if err := uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
			return tx.Outbox().Enqueue(ctx, ports.StoreCardTask("t1", now.Add(delay)))
		}); err != nil {
			t.Fatal(err)
		}
	}
	var count int
	var next time.Time
	query := `SELECT count(*), min(next_at) FROM outbox WHERE kind = 'participant.store-card.refresh'`
	if err := pool.QueryRow(ctx, query).Scan(&count, &next); err != nil {
		t.Fatal(err)
	}
	if count != 1 || !next.Equal(now) {
		t.Fatal(count, next)
	}
}

func TestStoreCardMigrationBackfillsAndRemovesPendingTasks(t *testing.T) {
	pool := testPool(t)
	uow := postgres.NewUnitOfWork(pool)
	seedStoredCard(t, uow)
	ctx := context.Background()
	if err := postgres.MigrateDownTo(ctx, pool, 37); err != nil {
		t.Fatal(err)
	}
	if err := postgres.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	var count int
	query := `SELECT count(*) FROM outbox WHERE kind = 'participant.store-card.refresh' AND done_at IS NULL`
	if err := pool.QueryRow(ctx, query).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("задач после миграции: %d", count)
	}
	if err := postgres.MigrateDownTo(ctx, pool, 37); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, query).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("задач после отката: %d", count)
	}
}

func TestModerationBeforeFirstSummarySurvivesStaleRefresh(t *testing.T) {
	pool := testPool(t)
	uow := postgres.NewUnitOfWork(pool)
	stale := seedStoredCard(t, uow)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `DELETE FROM store_cards WHERE tenant_id = 't1'`); err != nil {
		t.Fatal(err)
	}
	service := app.NewService(uow, fake.NewClock(at(1)), &fake.IDs{})
	summary, err := service.StoreSummary(ctx, "t1")
	if err != nil || summary.Offers != 0 || summary.Categories == nil || summary.Showcase == nil {
		t.Fatal(summary, err)
	}
	decision := participant.Decision{Note: "Причина"}
	if _, err := service.ModerateCard(ctx, "t1", participant.CardSlogan, true, decision); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Shop(ctx, "shop"); !errors.Is(err, participant.ErrNotFound) {
		t.Fatal(err)
	}
	if err := uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		return tx.StoreCards().Save(ctx, stale)
	}); err != nil {
		t.Fatal(err)
	}
	header, err := service.Shop(ctx, "shop")
	if err != nil || header.Slogan != "" {
		t.Fatal(header, err)
	}
}
