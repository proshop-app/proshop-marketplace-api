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
	defaultCardAppearanceJSON = `{"background":"#125F67","logoBackground":"#E9EEE7","textColor":"white"}`
	appearanceMigrationSeed   = `UPDATE participants
 SET store_card = $2, card_ready = COALESCE(($2::jsonb #>> '{card,ready}')::boolean, false)
 WHERE tenant_id = $1`
	appearanceMigrationSeedProjection = `
 INSERT INTO store_cards (tenant_id, slug, name, offers, public, summary, header, card)
 VALUES ($1, $1, 'Магазин', 3, true, '{"offers":3}',
 '{"color":{"card":"#FF0000"},"initial":"М","logo":"logo"}',
 '{"color":{"card":"#FF0000"},"initial":"М","offers":3}')`
	appearanceMigrationSnapshot = `SELECT store_card = $2::jsonb, card_ready, version
 FROM participants WHERE tenant_id = $1`
	appearanceMigrationProjection = `SELECT
 header = jsonb_build_object('logo', 'logo', 'appearance', $2::jsonb)
 AND card = jsonb_build_object('offers', 3, 'appearance', $2::jsonb)
 AND summary = '{"offers":3}'::jsonb
 FROM store_cards WHERE tenant_id = $1 AND profile_version = $3`
	appearanceMigrationTasks = `SELECT count(*) FROM outbox WHERE tenant_id = $1
 AND kind IN ('participant.settings.sync', 'participant.store-card.refresh') AND done_at IS NULL AND attempts = 0`
)

func TestStoreCardAppearanceMigration(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	if err := postgres.MigrateDownTo(ctx, pool, 44); err != nil {
		t.Fatal(err)
	}
	uow := postgres.NewUnitOfWork(pool)
	cases := []struct {
		before string
		after  string
		ready  bool
	}{
		{
			before: `{"profile":{"name":"Имя","color":{"card":"#FF0000"},"logo":"logo"},
 "card":{"ready":false,"missing":["brandColor","brandColor"]},"hidden":[],"returns":{"days":14}}`,
			after: `{"profile":{"name":"Имя","logo":"logo","appearance":` + defaultCardAppearanceJSON + `},
 "card":{"ready":true,"missing":[]},"hidden":[],"returns":{"days":14}}`,
			ready: true,
		},
		{
			before: `{"profile":{"color":null},"card":{"ready":false,"missing":["contacts","brandColor","terms"]}}`,
			after: `{"profile":{"appearance":` + defaultCardAppearanceJSON + `},
 "card":{"ready":false,"missing":["contacts","terms"]}}`,
		},
		{
			before: `{"profile":null,"card":{"ready":false,"missing":[]}}`,
			after: `{"profile":{"appearance":` + defaultCardAppearanceJSON + `},
 "card":{"ready":false,"missing":[]}}`,
		},
		{
			before: `{}`,
			after:  `{"profile":{"appearance":` + defaultCardAppearanceJSON + `}}`,
		},
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
		if _, err := pool.Exec(ctx, appearanceMigrationSeed, string(tenant), item.before); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, appearanceMigrationSeedProjection, string(tenant)); err != nil {
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
	for range 2 {
		if err := migrateUpTo(ctx, pool, 45); err != nil {
			t.Fatal(err)
		}
		for i, item := range cases {
			tenant := fmt.Sprintf("t%d", i+1)
			var same, ready bool
			var version int64
			row := pool.QueryRow(ctx, appearanceMigrationSnapshot, tenant, item.after)
			if err := row.Scan(&same, &ready, &version); err != nil || !same || ready != item.ready || version != 2 {
				t.Fatal(tenant, same, ready, version, err)
			}
			row = pool.QueryRow(ctx, appearanceMigrationProjection, tenant, defaultCardAppearanceJSON, version)
			if err := row.Scan(&same); err != nil || !same {
				t.Fatal(tenant, same, err)
			}
			var tasks int
			if err := pool.QueryRow(ctx, appearanceMigrationTasks, tenant).Scan(&tasks); err != nil || tasks != 2 {
				t.Fatal(tenant, tasks, err)
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
		t.Fatal("старый снимок перезаписал оформление", err)
	}
}

func TestStoreCardAppearanceSurvivesStaleRefresh(t *testing.T) {
	uow := postgres.NewUnitOfWork(testPool(t))
	stale := seedStoredCard(t, uow)
	newer := stale
	newer.ProfileVersion++
	newer.Header.Appearance = participant.DefaultCardAppearance()
	newer.Card.Appearance = newer.Header.Appearance
	ctx := context.Background()
	if err := uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		if err := tx.StoreCards().Save(ctx, newer); err != nil {
			return err
		}
		return tx.StoreCards().Save(ctx, stale)
	}); err != nil {
		t.Fatal(err)
	}
	if err := uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		record, err := tx.StoreCards().Get(ctx, "t1")
		if err != nil {
			return err
		}
		if record.Header.Appearance != newer.Header.Appearance || record.Card.Appearance != newer.Card.Appearance {
			t.Fatal(record)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
