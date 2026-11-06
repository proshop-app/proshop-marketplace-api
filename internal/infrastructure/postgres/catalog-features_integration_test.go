//go:build integration

package postgres_test

import (
	"context"
	"testing"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/domain/moderation"
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/postgres"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
	"github.com/proshop-app/proshop-marketplace/pkg/money"
)

const (
	updateInterestStatus = `UPDATE checkout_groups SET order_progress = jsonb_build_object('Status', $2::text)
 WHERE checkout_id = $1`

	seedInterestCart = `INSERT INTO carts (id, buyer_id, version, created_at, updated_at, expires_at)
 VALUES ($1, $1, 1, now(), now(), now() + interval '1 day')`
	seedInterestLine = `INSERT INTO cart_lines
 (cart_id, tenant_id, product_id, position, name, quantity, price, added_at, checked_at)
 VALUES ($1, 't1', 'p1', 0, 'Товар', 1, 10000, now(), now())`
	seedInterestCheckout = `INSERT INTO checkouts
 (id, buyer_id, idempotency_key, status, goods_total, delivery_total, total, version, created_at, updated_at)
 VALUES ($1, $1, 'r2', 'paid', 10000, 0, 10000, 1, now(), now())`
	seedInterestOrder = `INSERT INTO checkout_groups
 (checkout_id, tenant_id, external_checkout_id, lines, goods, order_progress)
 VALUES ($1, 't2', 'r2', '[{"productId":"p1"}]', 10000, '{"Status":"paid"}')`
)

func seedRecommendationTenant(t *testing.T, uow *postgres.UnitOfWork, tenant ids.TenantID) {
	t.Helper()
	seedTenant(t, uow, tenant, string(tenant), participant.TenantActive)
	if err := uow.Do(context.Background(), func(ctx context.Context, tx ports.Tx) error {
		member := participant.Restore(participant.State{
			TenantID:  tenant,
			Slug:      string(tenant),
			Name:      "Магазин",
			Stage:     participant.StageConnected,
			Enabled:   true,
			Trust:     participant.Probation,
			Delivery:  w3RepositoriesIntegrationDelivery,
			Subsidy:   w3RepositoriesIntegrationSubsidy,
			Version:   1,
			CreatedAt: at(0),
			UpdatedAt: at(0),
		})
		if err := tx.Participants().Add(ctx, member); err != nil {
			return err
		}
		for i, id := range []ids.ProductID{"p1", "p2", "p3"} {
			snap := snapshot(tenant, id, 1, 0)
			if id == "p3" {
				// p1 и p2 — варианты одной модели, p3 — сам по себе: выдача сворачивает группу до одного.
				snap.Card.VariantsID = ""
			}
			snap.Card.Publication = taxonomy.Publication{Published: true, CategoryID: "c1"}
			snap.AverageRating, snap.RatingsCount = 4.5, 12
			snap.SalesCount = int64(10 - i)
			snap.PriceProfiles[0].EffectivePrice = money.FromKopecks(10000 + int64(i)*5000)
			product, err := catalog.NewProduct(snap, at(0))
			if err != nil {
				return err
			}
			if err := tx.Products().Save(ctx, product); err != nil {
				return err
			}
			verdict := moderation.NewVerdict(tenant, id)
			verdict.Evaluate(1, 1, moderation.Rules{Revision: 1}, moderation.Input{Policy: "auto"}, at(0))
			if err := tx.ModerationVerdicts().Save(ctx, verdict); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestCatalogRecommendationsProjection(t *testing.T) {
	pool := testPool(t)
	uow := postgres.NewUnitOfWork(pool)
	ctx := context.Background()
	seedRecommendationTenant(t, uow, "t1")
	seedRecommendationTenant(t, uow, "t2")
	repository := postgres.NewCatalogFeatures(pool)
	low, high := int64(6000), int64(14000)
	filter := ports.RecommendationFilter{
		Categories:  []string{"c1"},
		Excluded:    []ids.OfferID{{Tenant: "t1", Product: "p1"}},
		OtherTenant: "t1",
		Region:      "RU-MOW",
		PriceMin:    &low,
		PriceMax:    &high,
		Limit:       20,
	}
	states, err := repository.Recommend(ctx, filter)
	if err != nil || len(states) != 1 {
		t.Fatal(states, err)
	}
	state := states[0].Product.State()
	if state.TenantID != "t2" || state.ProductID != "p1" || state.AverageRating != 4.5 || state.RatingsCount != 12 {
		t.Fatal(state)
	}

	popular, err := repository.Recommend(ctx, ports.RecommendationFilter{Limit: 6})
	if err != nil || len(popular) != 4 {
		t.Fatal(popular, err)
	}
	for i := 1; i < len(popular); i++ {
		if popular[i-1].Product.State().SalesCount < popular[i].Product.State().SalesCount {
			t.Fatal(popular)
		}
	}
	other, err := repository.Recommend(ctx, ports.RecommendationFilter{OtherTenant: "t1", Limit: 1})
	if err != nil || len(other) != 1 || other[0].Product.State().TenantID != "t2" {
		t.Fatal(other, err)
	}
	wrong, err := repository.Recommend(ctx, ports.RecommendationFilter{Categories: []string{"missing"}, Limit: 20})
	if err != nil || len(wrong) != 0 {
		t.Fatal(wrong, err)
	}
	filter.Region = "RU-SPE"
	if states, err := repository.Recommend(ctx, filter); err != nil || len(states) != 0 {
		t.Fatal(states, err)
	}
	filter.Region = "RU-MOW"
	filter.PriceContexts = map[ids.TenantID]string{"t2": "marketplace:missing"}
	if states, err := repository.Recommend(ctx, filter); err != nil || len(states) != 0 {
		t.Fatal(states, err)
	}
	seedBuyer(t, uow, buyerA, "79990000001")
	for _, sql := range []string{seedInterestCart, seedInterestLine, seedInterestCheckout, seedInterestOrder} {
		if _, err := pool.Exec(ctx, sql, string(buyerA)); err != nil {
			t.Fatal(err)
		}
	}
	interests, err := repository.Interests(ctx, buyerA)
	if err != nil || len(interests.Categories) != 1 || interests.Categories[0] != "c1" ||
		len(interests.Purchased) != 1 || interests.Purchased[0].String() != "t2~p1" {
		t.Fatal(interests, err)
	}
	personal, err := repository.Recommend(ctx, ports.RecommendationFilter{
		Categories: interests.Categories,
		Excluded:   interests.Purchased,
		Limit:      20,
	})
	if err != nil || len(personal) != 3 {
		t.Fatal(personal, err)
	}
	for _, item := range personal {
		state := item.Product.State()
		if state.TenantID == "t2" && state.ProductID == "p1" {
			t.Fatal("купленное попало в рекомендации")
		}
	}
	interests, err = repository.Interests(ctx, buyerB)
	if err != nil || len(interests.Categories) != 0 || len(interests.Purchased) != 0 {
		t.Fatal(interests, err)
	}
}

func TestRatingRepositoryRevision(t *testing.T) {
	uow := postgres.NewUnitOfWork(testPool(t))
	ctx := context.Background()
	first := snapshot("t1", "p1", 1, 0)
	first.AverageRating, first.RatingsCount = 4.7, 20
	if err := uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		p, err := catalog.NewProduct(first, at(0))
		if err != nil {
			return err
		}
		return tx.Products().Save(ctx, p)
	}); err != nil {
		t.Fatal(err)
	}
	for revision := int64(1); revision <= 2; revision++ {
		if err := uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
			p, err := tx.Products().Get(ctx, "t1", "p1")
			if err != nil {
				return err
			}
			if p.State().AverageRating != 4.7 || p.State().RatingsCount != 20 {
				t.Fatal(p.State())
			}
			next := first
			next.Revision, next.AverageRating, next.RatingsCount = revision, 3.5, 30
			changed, err := p.Apply(next, at(1))
			if err != nil {
				return err
			}
			if changed != (revision == 2) {
				t.Fatal(changed)
			}
			return tx.Products().Save(ctx, p)
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		p, err := tx.Products().Get(ctx, "t1", "p1")
		if err != nil {
			return err
		}
		if p.State().AverageRating != 3.5 || p.State().RatingsCount != 30 {
			t.Fatal(p.State())
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestRecommendationsCollapseGroupsAndGeneratedColumns(t *testing.T) {
	pool := testPool(t)
	uow := postgres.NewUnitOfWork(pool)
	ctx := context.Background()
	seedRecommendationTenant(t, uow, "t1")
	if err := uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		for _, id := range []ids.ProductID{"p1", "p2"} {
			p, err := tx.Products().Get(ctx, "t1", id)
			if err != nil {
				return err
			}
			snap := snapshot("t1", id, 2, 0)
			snap.Card.VariantsID = "v1"
			snap.Card.Publication = taxonomy.Publication{Published: true, CategoryID: "c2"}
			snap.SalesCount = 20
			if _, err := p.Apply(snap, at(1)); err != nil {
				return err
			}
			if err := tx.Products().Save(ctx, p); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	repository := postgres.NewCatalogFeatures(pool)
	states, err := repository.Recommend(ctx, ports.RecommendationFilter{Categories: []string{"c2"}, Limit: 20})
	if err != nil || len(states) != 1 || states[0].Product.State().ProductID != "p1" {
		t.Fatal(states, err)
	}
	states, err = repository.Recommend(ctx, ports.RecommendationFilter{
		Categories: []string{"c2"},
		Excluded:   []ids.OfferID{{Tenant: "t1", Product: "p2"}},
		Limit:      20,
	})
	if err != nil || len(states) != 0 {
		t.Fatal(states, err)
	}
}

func TestInterestsIncludeUnpaidOrders(t *testing.T) {
	pool := testPool(t)
	uow := postgres.NewUnitOfWork(pool)
	ctx := context.Background()
	seedRecommendationTenant(t, uow, "t2")
	seedBuyer(t, uow, buyerA, "79990000001")
	if _, err := pool.Exec(ctx, seedInterestCheckout, string(buyerA)); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, seedInterestOrder, string(buyerA)); err != nil {
		t.Fatal(err)
	}
	repository := postgres.NewCatalogFeatures(pool)
	for _, status := range []string{"awaiting_payment", "creating_order"} {
		if _, err := pool.Exec(ctx, updateInterestStatus, string(buyerA), status); err != nil {
			t.Fatal(err)
		}
		interests, err := repository.Interests(ctx, buyerA)
		if err != nil || len(interests.Categories) != 1 || len(interests.Purchased) != 0 {
			t.Fatal(status, interests, err)
		}
	}
}
