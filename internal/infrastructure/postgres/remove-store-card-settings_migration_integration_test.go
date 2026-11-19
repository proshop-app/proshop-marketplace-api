//go:build integration

package postgres_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/postgres"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

const (
	retiredCardSeed = `UPDATE participants SET store_card = jsonb_build_object(
 'profile', jsonb_build_object('name', 'Магазин', 'logo', 'logo', 'appearance', '{"color":"#ABCDEF"}'::jsonb),
 'products', '{"enabled":false,"productIds":["pinned"]}'::jsonb,
 'card', jsonb_build_object('ready', false, 'missing', $2::jsonb),
 'returns', '{"days":14,"shipping":"seller","how":"Напишите"}'::jsonb,
 'hidden', '[{"key":"logo","reason":"Причина"}]'::jsonb), card_ready = false WHERE tenant_id = $1`
	retiredCardProjectionSeed = `INSERT INTO store_cards
 (tenant_id, slug, name, offers, public, page_public, summary, header, card) VALUES
 ($1, $1, 'Магазин', 3, false, true,
 '{"products":[{"productId":"automatic","offerId":"t1~automatic","name":"Товар","image":null}],
 "selectedProducts":[{"productId":"pinned"}],"offers":3}',
 '{"logo":"logo","appearance":{"color":"#ABCDEF"}}',
 '{"products":[],"appearance":{"color":"#ABCDEF"}}')`
	retiredCardSnapshot = `SELECT
 NOT (store_card ? 'products') AND NOT (store_card->'profile' ? 'appearance')
 AND store_card->'profile' = '{"name":"Магазин","logo":"logo"}'::jsonb
 AND store_card->'returns' = '{"days":14,"shipping":"seller","how":"Напишите"}'::jsonb
 AND store_card->'hidden' = '[{"key":"logo","reason":"Причина"}]'::jsonb,
 store_card #> '{card,missing}' = $2::jsonb, card_ready, version FROM participants WHERE tenant_id = $1`
	retiredCardProjection = `SELECT NOT (summary ? 'selectedProducts') AND NOT (header ? 'appearance')
 AND NOT (card ? 'appearance') AND card->'products' = summary->'products', public
 FROM store_cards WHERE tenant_id = $1`
	retiredCardDown = `SELECT store_card #> '{profile,appearance}' = '{}'::jsonb
 AND store_card->'products' = '{"enabled":true,"productIds":[]}'::jsonb FROM participants WHERE tenant_id = $1`
)

func TestRemoveStoreCardSettingsMigration(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	if err := postgres.MigrateDownTo(ctx, pool, 60); err != nil {
		t.Fatal(err)
	}
	uow := postgres.NewUnitOfWork(pool)
	cases := []struct {
		missing   string
		remaining string
		ready     bool
	}{
		{
			missing:   `["appearance","products","brandColor","cardAppearance","cardProducts"]`,
			remaining: `[]`,
			ready:     true,
		},
		{
			missing:   `["products","contacts","brandColor"]`,
			remaining: `["contacts"]`,
		},
	}
	for i, item := range cases {
		tenant := ids.TenantID(fmt.Sprintf("t%d", i+1))
		seedTenant(t, uow, tenant, string(tenant), participant.TenantActive)
		if err := uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
			state, err := tx.Registry().Get(ctx, tenant)
			if err != nil {
				return err
			}
			member, err := participant.New(state, "Магазин", at(0))
			if err != nil {
				return err
			}
			return tx.Participants().Add(ctx, member)
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, retiredCardSeed, string(tenant), item.missing); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, retiredCardProjectionSeed, string(tenant)); err != nil {
			t.Fatal(err)
		}
	}
	for range 2 {
		if err := postgres.Migrate(ctx, pool); err != nil {
			t.Fatal(err)
		}
		for i, item := range cases {
			tenant := fmt.Sprintf("t%d", i+1)
			var preserved, missing, ready bool
			var version int64
			row := pool.QueryRow(ctx, retiredCardSnapshot, tenant, item.remaining)
			if err := row.Scan(&preserved, &missing, &ready, &version); err != nil ||
				!preserved || !missing || ready != item.ready || version != 2 {
				t.Fatal(tenant, preserved, missing, ready, version, err)
			}
			row = pool.QueryRow(ctx, retiredCardProjection, tenant)
			if err := row.Scan(&preserved, &ready); err != nil || !preserved || ready != item.ready {
				t.Fatal(tenant, preserved, ready, err)
			}
		}
	}
	if err := postgres.MigrateDownTo(ctx, pool, 60); err != nil {
		t.Fatal(err)
	}
	var restored bool
	if err := pool.QueryRow(ctx, retiredCardDown, "t1").Scan(&restored); err != nil || !restored {
		t.Fatal("Down восстанавливает пустую структуру, прежние данные не возвращаются", restored, err)
	}
	if err := postgres.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
}
