//go:build integration

package postgres_test

import (
	"context"
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
			Target:        promotion.Target{OfferID: ids.OfferID{Tenant: "t1", Product: "p1"}, CategoryID: "c1"},
			Period:        promotion.Period{From: "2026-11-09", To: "2026-11-15"},
			Price:         money.FromKopecks(700),
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
		if _, err := read.Decide(promotion.Submit, "", "", promotion.Payment{}, now); err != nil {
			return err
		}
		if err := tx.Promotion().Save(ctx, read); err != nil {
			return err
		}
		d := promotion.Decision{At: now, Actor: "tenant", Action: promotion.Submit}
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
		weeks, err := tx.Promotion().Occupancy(ctx, request, promotion.Category, []string{"2026-11-09"})
		if err != nil {
			return err
		}
		if len(weeks) != 1 || weeks[0].Sold != 1 {
			t.Fatal(weeks)
		}
		for i := range 2 {
			fresh, err := tx.Promotion().Count(ctx, c.State(), "2026-11-09", "hash", "view")
			if err != nil {
				return err
			}
			if fresh != (i == 0) {
				t.Fatal(fresh)
			}
		}
		rows, err := tx.Promotion().Stats(ctx, c.State().ID, "", "")
		if err != nil {
			return err
		}
		if len(rows) != 1 || rows[0].Impressions != 1 {
			t.Fatal(rows)
		}
		decisions, err := tx.Promotion().Decisions(ctx, c.State().ID)
		if err != nil {
			return err
		}
		if len(decisions) != 1 || decisions[0].Actor != "tenant" {
			t.Fatal(decisions)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
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
						Target:        promotion.Target{OfferID: ids.OfferID{Tenant: "t1", Product: "p1"}},
						Period:        promotion.Period{From: "2026-11-09", To: "2026-11-15"},
						Price:         money.FromKopecks(100),
					},
					now,
					time.UTC,
				)
				if err != nil {
					return err
				}
				weeks, err := tx.Promotion().Occupancy(ctx, c.State(), promotion.Global, []string{"2026-11-09"})
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
