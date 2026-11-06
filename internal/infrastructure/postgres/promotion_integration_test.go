//go:build integration

package postgres_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/promotion"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/postgres"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/postgres/pgtest"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
	"github.com/proshop-app/proshop-marketplace/pkg/money"
)

const (
	promotionLegacyInsert = `INSERT INTO promotion_campaigns
 (id,tenant_id,kind,placement_code,position,category_id,date_from,date_to,price,payment_amount,status,version,state)
 VALUES($1,'t1','sponsored_slot','category_slot',1,'c1','2026-11-09','2026-11-15',700,0,'draft',1,$2)`
	promotionColumnsCheck = `SELECT created_at,updated_at,product_id FROM promotion_campaigns WHERE id=$1`
)

func TestPromotionRepositories(t *testing.T) {
	pool := pgtest.Pool(t)
	uow := postgres.NewUnitOfWork(pool)
	ctx := context.Background()
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	c, err := promotion.NewCampaign(
		promotion.CampaignState{
			ID:            "00000000-0000-0000-0000-000000000001",
			TenantID:      "t1",
			Kind:          promotion.SponsoredSlot,
			Marking:       promotion.Marking{Advertiser: "Рекламодатель", INN: "1234567890"},
			PlacementCode: "category_slot",
			Position:      1,
			Target: promotion.Target{
				OfferID:    ids.OfferID{Tenant: "t1", Product: "p1"},
				CategoryID: "c1",
			},
			Period: promotion.Period{From: "2026-11-09", To: "2026-11-15"},
			Price:  money.FromKopecks(700),
		},
		now,
		time.UTC,
	)
	if err != nil {
		t.Fatal(err)
	}
	err = uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		all, err := tx.Promotion().Placements(ctx)
		if err != nil {
			return err
		}
		if len(all) != 8 {
			t.Fatal(all)
		}
		for _, p := range all {
			if p.Active || p.DefaultPrice.Kopecks() != 0 || p.SlotLimit != 1 {
				t.Fatal(p)
			}
		}
		p, err := tx.Promotion().Placement(ctx, "category_slot")
		if err != nil {
			return err
		}
		if _, err := p.Configure(1, money.FromKopecks(700), nil, true); err != nil {
			return err
		}
		if err := tx.Promotion().SavePlacement(ctx, p); err != nil {
			return err
		}
		return tx.Promotion().Save(ctx, c)
	})
	if err != nil {
		t.Fatal(err)
	}
	stale := promotion.RestoreCampaign(c.State())
	err = uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		read, err := tx.Promotion().Get(ctx, c.State().ID)
		if err != nil {
			return err
		}
		if read.State().Price.Kopecks() != 700 {
			t.Fatal(read.State())
		}
		if _, err := read.Decide(promotion.Submit, "", "", promotion.Payment{}, now.Add(time.Hour)); err != nil {
			return err
		}
		if err := tx.Promotion().Save(ctx, read); err != nil {
			return err
		}
		d := promotion.Decision{At: now, Actor: promotion.ActorTenant, Action: promotion.Submit}
		return tx.Promotion().AddDecision(ctx, read.State().ID, d)
	})
	if err != nil {
		t.Fatal(err)
	}
	err = uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		if _, err := stale.Decide(promotion.Submit, "", "", promotion.Payment{}, now); err != nil {
			return err
		}
		return tx.Promotion().Save(ctx, stale)
	})
	if !errors.Is(err, promotion.ErrConflict) {
		t.Fatal(err)
	}
	err = uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		request := c.State()
		request.ID = "00000000-0000-0000-0000-000000000002"
		weeks, err := tx.Promotion().Occupancy(ctx, request, promotion.Category, []string{"2026-11-09"}, "")
		if err != nil {
			return err
		}
		if len(weeks) != 1 || weeks[0].Sold != 1 {
			t.Fatal(weeks)
		}
		for i := range 2 {
			for _, kind := range []string{"view", "click"} {
				fresh, err := tx.Promotion().Count(ctx, c.State(), "2026-11-09", "hash", kind)
				if err != nil {
					return err
				}
				if fresh != (i == 0) {
					t.Fatal(fresh)
				}
			}
		}
		rows, err := tx.Promotion().Stats(ctx, c.State().ID, "", "")
		if err != nil {
			return err
		}
		if len(rows) != 1 || rows[0].Impressions != 1 || rows[0].Clicks != 1 {
			t.Fatal(rows)
		}
		summaries, err := tx.Promotion().Summaries(ctx, []ids.UUID{c.State().ID})
		if err != nil {
			return err
		}
		if summaries[c.State().ID].Impressions != 1 || summaries[c.State().ID].Clicks != 1 {
			t.Fatal(summaries)
		}
		traffic, err := tx.Promotion().Traffic(ctx, "2026-11-09", "2026-11-15")
		if err != nil {
			return err
		}
		if len(traffic) != 1 || traffic[0].Clicks != 1 {
			t.Fatal(traffic)
		}
		groups, err := tx.Promotion().ReportGroups(ctx, "2026-11-09", "2026-11-15")
		if err != nil {
			return err
		}
		if len(groups) != 1 || groups[0].Count != 1 || groups[0].Price.Kopecks() != 700 {
			t.Fatal(groups)
		}
		decisions, err := tx.Promotion().Decisions(ctx, c.State().ID)
		if err != nil {
			return err
		}
		if len(decisions) != 1 || decisions[0].Actor != promotion.ActorTenant {
			t.Fatal(decisions)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	var created, updated time.Time
	var product string
	err = pool.QueryRow(ctx, promotionColumnsCheck, c.State().ID).Scan(&created, &updated, &product)
	if err != nil || !created.Equal(now) || !updated.Equal(now.Add(time.Hour)) || product != "p1" {
		t.Fatal(created, updated, product, err)
	}

}

func TestPromotionConcurrentCapacity(t *testing.T) {
	pool := pgtest.Pool(t)
	uow := postgres.NewUnitOfWork(pool)
	ctx := context.Background()
	now := time.Date(2026, 11, 6, 0, 0, 0, 0, time.UTC)
	results := make(chan error, 2)
	for _, id := range []ids.UUID{
		"00000000-0000-0000-0000-000000000001",
		"00000000-0000-0000-0000-000000000002",
	} {
		go func() {
			results <- uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
				if err := tx.Promotion().LockPlacement(ctx, "search_slot"); err != nil {
					return err
				}
				c, err := promotion.NewCampaign(
					promotion.CampaignState{
						ID:            id,
						TenantID:      "t1",
						Kind:          promotion.SponsoredSlot,
						Marking:       promotion.Marking{Advertiser: "Рекламодатель", INN: "1234567890"},
						PlacementCode: "search_slot",
						Position:      1,
						Target: promotion.Target{
							OfferID:    ids.OfferID{Tenant: "t1", Product: "p1"},
							CategoryID: "c1",
						},
						Period: promotion.Period{From: "2026-11-09", To: "2026-11-15"},
						Price:  money.FromKopecks(100),
					},
					now,
					time.UTC,
				)
				if err != nil {
					return err
				}
				weeks, err := tx.Promotion().Occupancy(ctx, c.State(), promotion.Global, []string{"2026-11-09"}, "")
				if err != nil {
					return err
				}
				if err := promotion.CheckCapacity(weeks, 1); err != nil {
					return err
				}
				if _, err := c.Decide(promotion.Submit, "", "", promotion.Payment{}, now); err != nil {
					return err
				}
				return tx.Promotion().Save(ctx, c)
			})
		}()
	}
	successes, conflicts := 0, 0
	for range 2 {
		err := <-results
		switch {
		case err == nil:
			successes++
		case errors.Is(err, promotion.ErrNoCapacity):
			conflicts++
		default:
			t.Fatal(err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatal(successes, conflicts)
	}
}

func TestPromotionMigrationAndQueries(t *testing.T) {
	pool := pgtest.Pool(t)
	ctx := context.Background()
	// Откат только новой миграции воспроизводит установленную M1.1.
	if err := postgres.MigrateDown(ctx, pool); err != nil {
		t.Fatal(err)
	}
	old := promotion.CampaignState{
		ID:            "00000000-0000-0000-0000-000000000001",
		TenantID:      "t1",
		Kind:          promotion.SponsoredSlot,
		PlacementCode: "category_slot",
		Position:      1,
		Target:        promotion.Target{OfferID: ids.OfferID{Tenant: "t1", Product: "p1"}, CategoryID: "c1"},
		Period:        promotion.Period{From: "2026-11-09", To: "2026-11-15"},
		Price:         money.FromKopecks(700),
		Status:        promotion.Draft,
		Version:       1,
		CreatedAt:     time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC),
		UpdatedAt:     time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC),
	}
	raw, err := json.Marshal(old)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, promotionLegacyInsert, old.ID, raw); err != nil {
		t.Fatal(err)
	}
	if err := postgres.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	var created, updated time.Time
	var product string
	if err := pool.QueryRow(ctx, promotionColumnsCheck, old.ID).Scan(&created, &updated, &product); err != nil {
		t.Fatal(err)
	}
	if !created.Equal(old.CreatedAt) || !updated.Equal(old.UpdatedAt) || product != "p1" {
		t.Fatal(created, updated, product)
	}
	uow := postgres.NewUnitOfWork(pool)
	err = uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		c, err := tx.Promotion().Get(ctx, old.ID)
		if err != nil {
			return err
		}
		if c.State() != old {
			t.Fatal(c.State(), old)
		}
		newer := old
		newer.ID = "00000000-0000-0000-0000-000000000002"
		newer.CreatedAt = old.CreatedAt.Add(time.Hour)
		newer.UpdatedAt = newer.CreatedAt
		draft, err := promotion.NewCampaign(newer, newer.CreatedAt, time.UTC)
		if err != nil {
			return err
		}
		if err := tx.Promotion().Save(ctx, draft); err != nil {
			return err
		}
		rows, total, err := tx.Promotion().List(ctx, promotion.Filter{TenantID: "t1", ProductID: "p1", Limit: 1})
		if err != nil {
			return err
		}
		if total != 2 || len(rows) != 1 || rows[0].ID != newer.ID {
			t.Fatal(rows, total)
		}
		counts, err := tx.Promotion().Counts(ctx, promotion.Filter{ProductID: "p1", Limit: 1, Offset: 1})
		if err != nil {
			return err
		}
		if counts[promotion.Draft] != 2 {
			t.Fatal(counts)
		}
		if err := tx.Promotion().AddDecision(ctx, old.ID, promotion.Decision{
			At:     old.CreatedAt,
			Actor:  promotion.ActorTenant,
			Action: promotion.Revise,
		}); err != nil {
			return err
		}
		if err := tx.Promotion().Delete(ctx, c); err != nil {
			return err
		}
		history, err := tx.Promotion().Decisions(ctx, old.ID)
		if err != nil {
			return err
		}
		if len(history) != 0 {
			t.Fatal(history)
		}
		_, err = tx.Promotion().Count(ctx, newer, "2026-11-09", "visitor", "view")
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	retention := postgres.NewRetention(pool)
	result, err := retention.Purge(ctx, ports.PurgeRequest{
		PromotionBefore: time.Date(2026, 11, 15, 0, 0, 0, 0, time.UTC),
		Batch:           100,
	})
	if err != nil || result.PromotionViews != 0 {
		t.Fatal(result, err)
	}
	result, err = retention.Purge(ctx, ports.PurgeRequest{
		PromotionBefore: time.Date(2026, 11, 16, 0, 0, 0, 0, time.UTC),
		Batch:           100,
	})
	if err != nil || result.PromotionViews != 1 {
		t.Fatal(result, err)
	}
	// Down удаляет кликовый дедуп перед восстановлением прежнего первичного ключа.
	if err := postgres.MigrateDown(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if err := postgres.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
}
