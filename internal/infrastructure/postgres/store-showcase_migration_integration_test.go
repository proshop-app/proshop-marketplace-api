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

const (
	showcaseMigrationSeed = `UPDATE participants
 SET store_card = $2, card_ready = COALESCE(($2::jsonb #>> '{card,ready}')::boolean, false)
 WHERE tenant_id = $1`
	showcaseMigrationSeedProjection = `
 INSERT INTO store_cards (tenant_id, slug, name, offers, public, summary, header, card)
 VALUES ($1, $1, 'Магазин', 3, true,
 '{"offers":3,"showcase":[{"offerId":"t1:p1"}],"hidden":[{"key":"logo","reason":"Причина"}]}',
 '{"showcase":[{"offerId":"t1:p1","image":null}],"logo":"logo","name":"Магазин"}',
 '{"showcase":[{"offerId":"t1:p1","image":null}],"logo":"logo","offers":3}')`
	showcaseMigrationSeedTasks = `
 INSERT INTO outbox (kind, tenant_id, dedup_key, payload, next_at, created_at, attempts, done_at)
 SELECT 'participant.store-card.refresh', tenant_id, 'participant.store-card.refresh:' || tenant_id,
 '{}', now(), now(), CASE WHEN tenant_id = 't2' THEN 1 ELSE 0 END,
 CASE WHEN tenant_id = 't3' THEN now() ELSE NULL END
 FROM participants WHERE tenant_id IN ('t1', 't2', 't3')`
	showcaseMigrationQuery = `SELECT store_card = $2::jsonb, card_ready, version
 FROM participants WHERE tenant_id = $1`
	showcaseMigrationProjection = `
 SELECT header = '{"logo":"logo","name":"Магазин"}'::jsonb
 AND card = '{"logo":"logo","offers":3}'::jsonb
 AND summary = '{"offers":3,"hidden":[{"key":"logo","reason":"Причина"}]}'::jsonb
 FROM store_cards WHERE tenant_id = $1 AND profile_version = $2`
	showcaseMigrationTaskCount = `SELECT count(*) FROM outbox`
	showcaseMigrationTasks     = `SELECT count(*) FROM outbox WHERE tenant_id = $1
 AND kind = 'participant.store-card.refresh' AND dedup_key = 'participant.store-card.refresh:' || tenant_id
 AND attempts = 0 AND done_at IS NULL AND payload = '{}'::jsonb AND next_at <= now()`
)

func TestRemoveStoreShowcaseMigration(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	if err := postgres.MigrateDownTo(ctx, pool, 43); err != nil {
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
 "profile":{"name":"Магазин","showcase":{"mode":"pinned","productIds":["p1"]},"logo":"logo"},
 "card":{"ready":false,"missing":["showcase","showcase"]},
 "hidden":[{"key":"logo","reason":"Логотип"}],
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
 "profile":{"showcase":{"mode":"auto","productIds":[]}},
 "card":{"ready":false,"missing":["requisites","showcase","terms"]},
 "hidden":[]
}`,
			after:   `{"profile":{},"card":{"ready":false,"missing":["requisites","terms"]},"hidden":[]}`,
			version: 2,
		},
		{
			before:  `{"profile":{"showcase":null},"card":{"ready":true,"missing":[]},"hidden":null}`,
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

		if _, err := pool.Exec(ctx, showcaseMigrationSeed, string(tenantID), item.before); err != nil {
			t.Fatal(err)
		}

		if _, err := pool.Exec(ctx, showcaseMigrationSeedProjection, string(tenantID)); err != nil {
			t.Fatal(err)
		}
	}
	// Проверяем дедупликацию, не мешая новой задаче догнать уже взятую или выполненную.
	if _, err := pool.Exec(ctx, showcaseMigrationSeedTasks); err != nil {
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
		if err := migrateUpTo(ctx, pool, 44); err != nil {
			t.Fatal(err)
		}
		for i, item := range cases {
			tenantID := fmt.Sprintf("t%d", i+1)
			var same, ready bool
			var version int64

			row := pool.QueryRow(ctx, showcaseMigrationQuery, tenantID, item.after)
			if err := row.Scan(&same, &ready, &version); err != nil {
				t.Fatal(err)
			}
			if !same || ready != item.ready || version != item.version {
				t.Fatalf("проход %d, %s: снимок=%v готовность=%v версия=%d", pass, tenantID, same, ready, version)
			}

			row = pool.QueryRow(ctx, showcaseMigrationProjection, tenantID, item.version)
			if err := row.Scan(&same); err != nil || !same {
				t.Fatal(tenantID, same, err)
			}
			var count int

			if err := pool.QueryRow(ctx, showcaseMigrationTasks, tenantID).Scan(&count); err != nil || count != 1 {
				t.Fatal(tenantID, count, err)
			}
		}
		var count int
		if err := pool.QueryRow(ctx, showcaseMigrationTaskCount).Scan(&count); err != nil || count != 8 {
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
			if err := postgres.MigrateDownTo(ctx, pool, 43); err != nil {
				t.Fatal(err)
			}
		}
	}
}
