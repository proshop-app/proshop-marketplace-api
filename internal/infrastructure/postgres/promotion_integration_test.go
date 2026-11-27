//go:build integration

package postgres_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/promotion"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/postgres"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/postgres/pgtest"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
	"github.com/proshop-app/proshop-marketplace/pkg/money"
	"github.com/proshop-app/proshop-marketplace/pkg/safego"
)

const (
	promotionLegacyInsert = `INSERT INTO promotion_campaigns
 (id,tenant_id,kind,placement_code,position,category_id,date_from,date_to,price,payment_amount,status,version,state)
 VALUES($1,'t1','sponsored_slot','category_slot',1,'c1','2026-11-09','2026-11-15',700,0,'draft',1,$2)`
	activatePromotionForCounters = `UPDATE promotion_campaigns SET status = 'active' WHERE id = $1`
	promotionColumnsCheck        = `SELECT created_at,updated_at,product_id FROM promotion_campaigns WHERE id=$1`
	promotionCountShards         = `SELECT count(*) FROM promotion_counters WHERE campaign_id=$1 AND day=$2::text::date`
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
			return fmt.Errorf("%+v", all)
		}
		for _, p := range all {
			if p.Active || p.DefaultPrice.Kopecks() != 0 || p.SlotLimit != 1 {
				return fmt.Errorf("%+v", p)
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
			return fmt.Errorf("%+v", read.State())
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
	if _, err := pool.Exec(ctx, activatePromotionForCounters, c.State().ID); err != nil {
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
			return fmt.Errorf("%+v", weeks)
		}
		events := []promotion.Beacon{
			{
				EventID:     "10000000-0000-4000-8000-000000000001",
				PromotionID: c.State().ID,
				Kind:        "view",
			},
			{
				EventID:     "10000000-0000-4000-8000-000000000002",
				PromotionID: c.State().ID,
				Kind:        "click",
			},
		}
		events = append(events, events[0])
		day := "2026-11-09"
		fresh, err := tx.Promotion().CountEvents(ctx, day, events)
		if err != nil || len(fresh) != 2 {
			return fmt.Errorf("первая пачка: %+v %w", fresh, err)
		}
		fresh, err = tx.Promotion().CountEvents(ctx, "2026-11-10", events)
		if err != nil || len(fresh) != 0 {
			return fmt.Errorf("повтор пачки: %+v %w", fresh, err)
		}
		rows, err := tx.Promotion().Stats(ctx, c.State().ID, "", "")
		if err != nil {
			return err
		}
		if len(rows) != 1 || rows[0].Impressions != 1 || rows[0].Clicks != 1 {
			return fmt.Errorf("%+v", rows)
		}
		summaries, err := tx.Promotion().Summaries(ctx, []ids.UUID{c.State().ID})
		if err != nil {
			return err
		}
		if summaries[c.State().ID].Impressions != 1 || summaries[c.State().ID].Clicks != 1 {
			return fmt.Errorf("%+v", summaries)
		}
		traffic, err := tx.Promotion().Traffic(ctx, "2026-11-09", "2026-11-15")
		if err != nil {
			return err
		}
		if len(traffic) != 1 || traffic[0].Clicks != 1 {
			return fmt.Errorf("%+v", traffic)
		}
		groups, err := tx.Promotion().ReportGroups(ctx, "2026-11-09", "2026-11-15")
		if err != nil {
			return err
		}
		if len(groups) != 1 || groups[0].Count != 1 || groups[0].Price.Kopecks() != 700 {
			return fmt.Errorf("%+v", groups)
		}
		decisions, err := tx.Promotion().Decisions(ctx, c.State().ID)
		if err != nil {
			return err
		}
		if len(decisions) != 1 || decisions[0].Actor != promotion.ActorTenant {
			return fmt.Errorf("%+v", decisions)
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
	var shards int
	if err := pool.QueryRow(ctx, promotionCountShards, c.State().ID, "2026-11-09").Scan(&shards); err != nil {
		t.Fatal(err)
	}
	if shards != 1 {
		t.Fatalf("одна пачка должна использовать один шард, использовано %d", shards)
	}
	if err := countConcurrentPromotionBatches(ctx, uow, c.State().ID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, promotionCountShards, c.State().ID, "2026-11-09").Scan(&shards); err != nil {
		t.Fatal(err)
	}
	if shards < 32 {
		t.Fatalf("5000 маячков использовали %d из 64 шардов", shards)
	}
}

func countConcurrentPromotionBatches(ctx context.Context, uow ports.UnitOfWork, campaign ids.UUID) error {
	const (
		batches   = 100
		batchSize = 50
		day       = "2026-11-09"
	)
	group, batchCtx := errgroup.WithContext(ctx)
	group.SetLimit(12)
	for i := range batches {
		group.Go(safego.Guard("test.promotion.beacons", func() error {
			events := make([]promotion.Beacon, batchSize)
			for j := range events {
				events[j] = promotion.Beacon{
					EventID:     ids.UUID(fmt.Sprintf("20000000-0000-4000-8000-%012d", i*batchSize+j)),
					PromotionID: campaign,
					Kind:        "view",
				}
			}
			return uow.Do(batchCtx, func(ctx context.Context, tx ports.Tx) error {
				fresh, err := tx.Promotion().CountEvents(ctx, day, events)
				if err != nil || len(fresh) != batchSize {
					return fmt.Errorf("новые маячки пачки %d: %d, %w", i, len(fresh), err)
				}
				repeated, err := tx.Promotion().CountEvents(ctx, "2026-11-10", events)
				if err != nil || len(repeated) != 0 {
					return fmt.Errorf("повторы пачки %d: %d, %w", i, len(repeated), err)
				}
				return nil
			})
		}))
	}
	if err := group.Wait(); err != nil {
		return err
	}
	return uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		rows, err := tx.Promotion().Stats(ctx, campaign, "", "")
		if err != nil {
			return err
		}
		if len(rows) != 1 || rows[0].Impressions != batches*batchSize+1 || rows[0].Clicks != 1 {
			return fmt.Errorf("итоги кампании после параллельных пачек: %+v", rows)
		}
		return nil
	})
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
	// Версия 35 воспроизводит установленную M1.1 до добавления колонок размещения.
	if err := postgres.MigrateDownTo(ctx, pool, 35); err != nil {
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
			return fmt.Errorf("%+v %+v", c.State(), old)
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
			return fmt.Errorf("%+v %+v", rows, total)
		}
		counts, err := tx.Promotion().Counts(ctx, promotion.Filter{ProductID: "p1", Limit: 1, Offset: 1})
		if err != nil {
			return err
		}
		if counts[promotion.Draft] != 2 {
			return fmt.Errorf("%+v", counts)
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
			return fmt.Errorf("%+v", history)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	newID := ids.UUID("00000000-0000-0000-0000-000000000002")
	if _, err := pool.Exec(ctx, activatePromotionForCounters, newID); err != nil {
		t.Fatal(err)
	}
	if err := uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		events := []promotion.Beacon{{
			EventID:     "10000000-0000-4000-8000-000000000001",
			PromotionID: newID,
			Kind:        "view",
		}}
		_, err := tx.Promotion().CountEvents(ctx, "2026-11-09", events)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	retention := postgres.NewRetention(postgres.Single(pool))
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
	if err := postgres.MigrateDownTo(ctx, pool, 35); err != nil {
		t.Fatal(err)
	}
	if err := postgres.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
}
