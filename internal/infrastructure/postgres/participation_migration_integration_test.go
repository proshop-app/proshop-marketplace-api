//go:build integration

package postgres_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/postgres"
)

func TestOrderSettingsMigrationDisablesLegacyParticipation(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	if err := postgres.MigrateDownTo(ctx, pool, 20); err != nil {
		t.Fatal(err)
	}

	// Старое согласие и субсидия есть, но обязательные настройки ещё не заданы.
	seed := `
  ALTER TABLE participants ADD COLUMN payment_account_id text;
  ALTER TABLE participants ADD CONSTRAINT participants_payment_account_status
   CHECK ((status = 'connected') = (payment_account_id IS NOT NULL));

  INSERT INTO tenants (tenant_id, slug, status, event_at, version, updated_at)
  VALUES ('t1', 's', 'active', now(), 1, now());

  INSERT INTO participants (
   tenant_id, slug, name, status, enabled, shop_enabled, subsidy,
   settings_synced_at, payment_account_id, version, created_at, updated_at
  )
  VALUES (
   't1', 's', 'Магазин', 'connected', true, true,
   '{"enabled":true,"type":"percent","value":50,"maxAmount":30000}',
   now(), 'legacy-account', 1, now(), now()
  )`
	if _, err := pool.Exec(ctx, seed); err != nil {
		t.Fatal(err)
	}
	if err := postgres.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}

	uow := postgres.NewUnitOfWork(pool)
	err := uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		p, err := tx.Participants().Get(ctx, "t1")
		if err != nil {
			return err
		}
		tenant, err := tx.Registry().Get(ctx, "t1")
		if err != nil {
			return err
		}
		state := p.State()
		visibility := participant.VisibilityOf(p, tenant)
		if p.Version() != 2 || p.Enabled() || p.Stage() != participant.StageCandidate || !state.AwaitingReview ||
			p.DeliveryValid() || visibility.Visible || visibility.CheckoutAvailable {
			return fmt.Errorf("старое согласие без обязательных настроек выключено: %+v %+v", state, visibility)
		}
		if state.Subsidy.Value != 50 || !state.FreeDeliveryFrom.IsZero() || state.Delivery.Validate() == nil {
			return fmt.Errorf("субсидия сохранена, новые настройки продавец ещё не задал: %+v", state)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	var (
		tasks          int
		ready          bool
		accountRemoved bool
	)
	task := `
  SELECT count(*), bool_and(next_at <= now())
  FROM outbox
  WHERE kind = 'catalog.tenant.sync' AND tenant_id = 't1' AND done_at IS NULL`
	if err := pool.QueryRow(ctx, task).Scan(&tasks, &ready); err != nil {
		t.Fatal(err)
	}
	if tasks != 1 || !ready {
		t.Fatalf("переиндексация выключенного магазина готова: %d %v", tasks, ready)
	}
	removed := `
  SELECT NOT EXISTS (
   SELECT 1 FROM information_schema.columns
   WHERE table_schema = current_schema() AND table_name = 'participants' AND column_name = 'payment_account_id'
  )`
	if err := pool.QueryRow(ctx, removed).Scan(&accountRemoved); err != nil {
		t.Fatal(err)
	}
	if !accountRemoved {
		t.Fatal("старая платёжная привязка должна быть удалена")
	}
}
