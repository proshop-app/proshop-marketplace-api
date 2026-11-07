//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/postgres"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

func TestRemoveStoreSloganMigration(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	if err := postgres.MigrateDownTo(ctx, pool, 42); err != nil {
		t.Fatal(err)
	}
	uow := postgres.NewUnitOfWork(pool)
	cases := []struct {
		before  string
		after   string
		ready   bool
		version int64
	}{
		{
			before: `{
 "profile":{"name":"Магазин","slogan":"Текст","logo":"logo"},
 "card":{"ready":false,"missing":["slogan","slogan"]},
 "hidden":[{"key":"slogan","reason":"Текст"},{"key":"logo","reason":"Логотип"}],
 "returns":{"days":14}
}`,
			after: `{
 "profile":{"name":"Магазин","logo":"logo"},
 "card":{"ready":true,"missing":[]},
 "hidden":[{"key":"logo","reason":"Логотип"}],"returns":{"days":14}
}`,
			ready:   true,
			version: 2,
		},
		{
			before: `{
 "profile":{"slogan":"Текст"},"card":{"ready":false,"missing":["requisites","slogan","terms"]},
 "hidden":[{"key":"slogan"}]
}`,
			after:   `{"profile":{},"card":{"ready":false,"missing":["requisites","terms"]},"hidden":[]}`,
			version: 2,
		},
		{
			before:  `{"profile":{"slogan":""},"card":{"ready":true,"missing":[]},"hidden":null}`,
			after:   `{"profile":{},"card":{"ready":true,"missing":[]},"hidden":null}`,
			ready:   true,
			version: 2,
		},
		{before: `{}`, after: `{}`, version: 1},
		{
			before:  `{"profile":null,"card":{"ready":false,"missing":null},"hidden":[]}`,
			after:   `{"profile":null,"card":{"ready":false,"missing":null},"hidden":[]}`,
			version: 1,
		},
		{
			before:  `{"profile":{"logo":"logo"},"card":{"ready":true,"missing":[]},"hidden":[]}`,
			after:   `{"profile":{"logo":"logo"},"card":{"ready":true,"missing":[]},"hidden":[]}`,
			ready:   true,
			version: 1,
		},
	}
	for i, item := range cases {
		tenantID := ids.TenantID(fmt.Sprintf("t%d", i+1))
		seedTenant(t, uow, tenantID, string(tenantID), participant.TenantActive)
		if err := uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
			tenant, err := tx.Registry().Get(ctx, tenantID)
			if err != nil {
				return err
			}
			p, err := participant.New(tenant, "Магазин", at(0))
			if err != nil {
				return err
			}
			return tx.Participants().Add(ctx, p)
		}); err != nil {
			t.Fatal(err)
		}
		seed := `UPDATE participants
 SET store_card = $2, card_ready = COALESCE(($2::jsonb #>> '{card,ready}')::boolean, false)
 WHERE tenant_id = $1`
		if _, err := pool.Exec(ctx, seed, string(tenantID), item.before); err != nil {
			t.Fatal(err)
		}
		seedProjection := `
 INSERT INTO store_cards (tenant_id, slug, name, offers, public, summary, header, card)
 VALUES ($1, $1, 'Магазин', 3, true,
 '{"offers":3,"hidden":[{"key":"slogan"},{"key":"logo","reason":"Причина"},{"key":"slogan"}]}',
 '{"slogan":"Текст","logo":"logo","name":"Магазин"}',
 '{"slogan":"Текст","logo":"logo","offers":3}')`
		if _, err := pool.Exec(ctx, seedProjection, string(tenantID)); err != nil {
			t.Fatal(err)
		}
	}
	// Проверяем дедупликацию, не мешая новой задаче догнать уже взятую или выполненную.
	seedTasks := `
 INSERT INTO outbox (kind, tenant_id, dedup_key, payload, next_at, created_at, attempts, done_at)
 SELECT 'participant.store-card.refresh', tenant_id, 'participant.store-card.refresh:' || tenant_id,
 '{}', now(), now(), CASE WHEN tenant_id = 't2' THEN 1 ELSE 0 END,
 CASE WHEN tenant_id = 't3' THEN now() ELSE NULL END
 FROM participants WHERE tenant_id IN ('t1', 't2', 't3')`
	if _, err := pool.Exec(ctx, seedTasks); err != nil {
		t.Fatal(err)
	}
	var stale *participant.Participant
	if err := uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		var err error
		stale, err = tx.Participants().Get(ctx, "t1")
		return err
	}); err != nil {
		t.Fatal(err)
	}

	for pass := range 2 {
		if err := postgres.Migrate(ctx, pool); err != nil {
			t.Fatal(err)
		}
		for i, item := range cases {
			tenantID := fmt.Sprintf("t%d", i+1)
			var same, ready bool
			var version int64
			query := `SELECT store_card = $2::jsonb, card_ready, version FROM participants WHERE tenant_id = $1`
			if err := pool.QueryRow(ctx, query, tenantID, item.after).Scan(&same, &ready, &version); err != nil {
				t.Fatal(err)
			}
			if !same || ready != item.ready || version != item.version {
				t.Fatalf("проход %d, %s: снимок=%v готовность=%v версия=%d", pass, tenantID, same, ready, version)
			}
			projection := `
 SELECT header = '{"logo":"logo","name":"Магазин"}'::jsonb
 AND card = '{"logo":"logo","offers":3}'::jsonb
 AND summary = '{"offers":3,"hidden":[{"key":"logo","reason":"Причина"}]}'::jsonb
 FROM store_cards WHERE tenant_id = $1 AND profile_version = $2`
			if err := pool.QueryRow(ctx, projection, tenantID, item.version).Scan(&same); err != nil || !same {
				t.Fatal(tenantID, same, err)
			}
			var count int
			tasks := `SELECT count(*) FROM outbox WHERE tenant_id = $1
 AND kind = 'participant.store-card.refresh' AND dedup_key = 'participant.store-card.refresh:' || tenant_id
 AND attempts = 0 AND done_at IS NULL AND payload = '{}'::jsonb AND next_at <= now()`
			if err := pool.QueryRow(ctx, tasks, tenantID).Scan(&count); err != nil || count != 1 {
				t.Fatal(tenantID, count, err)
			}
		}
		var count int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM outbox`).Scan(&count); err != nil || count != 8 {
			t.Fatal(count, err)
		}
		if pass == 0 {
			if _, err := stale.HideCard(participant.CardLogo, "Другая причина", at(1)); err != nil {
				t.Fatal(err)
			}
			err := uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
				return tx.Participants().Update(ctx, stale)
			})
			if !errors.Is(err, participant.ErrConflict) {
				t.Fatalf("старый снимок перезаписал миграцию: %v", err)
			}
			if err := postgres.MigrateDownTo(ctx, pool, 42); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func TestRemoveStoreSloganMigrationSurvivesStaleRefresh(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	if err := postgres.MigrateDownTo(ctx, pool, 42); err != nil {
		t.Fatal(err)
	}
	uow := postgres.NewUnitOfWork(pool)
	stale := seedStoredCard(t, uow)
	stale.Summary.Hidden = []participant.CardHidden{{Key: "slogan", Reason: "Причина", At: at(0)}}
	if err := uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		return tx.StoreCards().Save(ctx, stale)
	}); err != nil {
		t.Fatal(err)
	}
	seed := `UPDATE participants
 SET store_card = jsonb_set(store_card, '{hidden}', '[{"key":"slogan","reason":"Причина"}]')
 WHERE tenant_id = 't1'`
	if _, err := pool.Exec(ctx, seed); err != nil {
		t.Fatal(err)
	}
	if err := postgres.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if err := uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		return tx.StoreCards().Save(ctx, stale)
	}); err != nil {
		t.Fatal(err)
	}
	if err := uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		record, err := tx.StoreCards().Get(ctx, "t1")
		if err != nil {
			return err
		}
		if len(record.Summary.Hidden) != 0 || record.ProfileVersion != 2 || record.Header.Logo == nil {
			return fmt.Errorf("старый пересчёт изменил очищенную карточку: %+v", record)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
