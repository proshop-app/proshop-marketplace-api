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
	colorMigrationDocumentTemplate = `{
 "profile":{"name":"Магазин","logo":"logo","appearance":%s},
 "card":{"ready":false,"missing":["contacts"]},
 "products":{"enabled":false,"productIds":["p1"]},
 "hidden":[{"key":"logo","reason":"Причина"}],"returns":{"days":14}
}`
	colorMigrationSeed = `UPDATE participants
 SET store_card = $2, card_ready = false WHERE tenant_id = $1`
	colorMigrationSeedProjection = `
 INSERT INTO store_cards (tenant_id, slug, name, offers, public, summary, header, card)
 VALUES ($1, $1, 'Магазин', 3, true, '{"offers":3}',
 jsonb_build_object('logo', 'logo', 'appearance', $2::jsonb),
 jsonb_build_object('offers', 3, 'appearance', $2::jsonb))`
	colorMigrationSnapshot = `SELECT store_card = jsonb_set(jsonb_set($2::jsonb, '{profile,appearance}', $3::jsonb),
 '{products}', '{"enabled":false,"productIds":[]}'::jsonb),
 card_ready, version FROM participants WHERE tenant_id = $1`
	colorMigrationProjection = `SELECT
 header = jsonb_build_object('logo', 'logo', 'appearance', $2::jsonb)
 AND card = jsonb_build_object('offers', 3, 'appearance', $2::jsonb, 'products', '[]'::jsonb)
 AND summary = '{"offers":3,"products":[],"selectedProducts":[]}'::jsonb
 FROM store_cards WHERE tenant_id = $1 AND profile_version = $3`
	colorMigrationTasks = `SELECT count(*) FROM outbox WHERE tenant_id = $1
 AND kind IN ('participant.settings.sync', 'participant.store-card.refresh') AND done_at IS NULL AND attempts = 0`
)

func TestStoreCardColorMigrationPreservesSelection(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	if err := postgres.MigrateDownTo(ctx, pool, 45); err != nil {
		t.Fatal(err)
	}
	uow := postgres.NewUnitOfWork(pool)
	cases := []struct {
		before  string
		color   string
		version int64
	}{
		{
			before:  `{"background":"#f3f3f3","logoBackground":"#E9EEE7","textColor":"white"}`,
			color:   "#F3F3F3",
			version: 2,
		},
		{
			before:  `{"color":"#abcdef","background":"#FF0000","textColor":"black"}`,
			color:   "#ABCDEF",
			version: 2,
		},
		{
			before:  `{"color":"invalid","background":"#FFFFFF","logoBackground":"#000000"}`,
			color:   "#FFFFFF",
			version: 2,
		},
		{before: `{"color":"#FFFF00"}`, color: "#FFFF00", version: 2},
		{before: `{"color":"#123","background":"red"}`, color: "#125F67", version: 2},
		{before: `null`, color: "#125F67", version: 2},
	}
	for i, item := range cases {
		tenant := ids.TenantID(fmt.Sprintf("t%d", i+1))
		seedTenant(t, uow, tenant, string(tenant), participant.TenantActive)
		if err := uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
			registry, err := tx.Registry().Get(ctx, tenant)
			if err != nil {
				return err
			}
			p, err := participant.New(registry, "Магазин", at(0))
			if err != nil {
				return err
			}
			return tx.Participants().Add(ctx, p)
		}); err != nil {
			t.Fatal(err)
		}
		before := colorMigrationDocument(item.before)
		if _, err := pool.Exec(ctx, colorMigrationSeed, string(tenant), before); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, colorMigrationSeedProjection, string(tenant), item.before); err != nil {
			t.Fatal(err)
		}
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
			tenant := fmt.Sprintf("t%d", i+1)
			appearance := `{"color":"` + item.color + `"}`
			var same, ready bool
			var version int64
			row := pool.QueryRow(ctx, colorMigrationSnapshot, tenant, colorMigrationDocument(item.before), appearance)
			if err := row.Scan(&same, &ready, &version); err != nil || !same || ready || version != item.version {
				t.Fatal(tenant, same, ready, version, err)
			}
			row = pool.QueryRow(ctx, colorMigrationProjection, tenant, appearance, version)
			if err := row.Scan(&same); err != nil || !same {
				t.Fatal(tenant, same, err)
			}
			var tasks int
			if err := pool.QueryRow(ctx, colorMigrationTasks, tenant).Scan(&tasks); err != nil || tasks != 2 {
				t.Fatal(tenant, tasks, err)
			}
		}
		if pass == 0 {
			if err := postgres.MigrateDownTo(ctx, pool, 45); err != nil {
				t.Fatal(err)
			}
		}
	}
	if _, err := stale.HideCard(participant.CardLogo, "Причина", at(1)); err != nil {
		t.Fatal(err)
	}
	err := uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		return tx.Participants().Update(ctx, stale)
	})
	if !errors.Is(err, participant.ErrConflict) {
		t.Fatal("старый снимок перезаписал выбранный цвет", err)
	}
}

func colorMigrationDocument(appearance string) string {
	return fmt.Sprintf(colorMigrationDocumentTemplate, appearance)
}
