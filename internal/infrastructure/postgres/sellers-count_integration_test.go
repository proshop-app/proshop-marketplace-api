//go:build integration

package postgres_test

import (
	"context"
	"testing"

	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/postgres"
)

const (
	setSellersModelKeys = `UPDATE catalog_products SET
        card = card || jsonb_build_object('modelKey', CASE WHEN product_id = 'p3' THEN 'other' ELSE 'model' END)`
	hideSellersParticipant = `UPDATE participants SET
        enabled = false,
        stage = 'candidate' WHERE tenant_id = 't2'`
	hideSellersProduct = `UPDATE catalog_products SET
        card = jsonb_set(card, '{visible}', 'false') WHERE tenant_id = 't2'`
	hideSellersPublication = `UPDATE catalog_products SET
        card = jsonb_set(card, '{publication,published}', 'false') WHERE tenant_id = 't2'`
	pendingSellersVerdict = `UPDATE moderation_verdicts SET status = 'pending' WHERE tenant_id = 't2'`
	deletedSellersProduct = `UPDATE catalog_products SET deleted = true WHERE tenant_id = 't2'`
	missingSellersPrice   = `UPDATE catalog_products SET price = NULL, base_price = NULL WHERE tenant_id = 't2'`
	hideSellersVerdict    = `DELETE FROM moderation_verdicts WHERE tenant_id = 't2'`
	hideSellersDelivery   = `UPDATE participants SET delivery_valid = false WHERE tenant_id = 't2'`
	hideSellersTenant     = `UPDATE tenants SET status = 'suspended' WHERE tenant_id = 't2'`
)

func TestSellersCountsProjection(t *testing.T) {
	pool := testPool(t)
	uow := postgres.NewUnitOfWork(pool)
	seedRecommendationTenant(t, uow, "t1")
	seedRecommendationTenant(t, uow, "t2")
	ctx := context.Background()
	if _, err := pool.Exec(ctx, setSellersModelKeys); err != nil {
		t.Fatal(err)
	}
	repository := postgres.NewSellersCounts(postgres.Single(pool))
	counts, err := repository.CountSellers(ctx, []string{"model", "other", "absent", "model"})
	if err != nil || counts["model"] != 2 || counts["other"] != 2 || counts["absent"] != 0 {
		t.Fatal(counts, err)
	}
	for _, query := range []string{
		hideSellersParticipant,
		hideSellersProduct,
		hideSellersPublication,
		hideSellersVerdict,
		pendingSellersVerdict,
		deletedSellersProduct,
		missingSellersPrice,
		hideSellersDelivery,
		hideSellersTenant,
	} {
		t.Run(query, func(t *testing.T) {
			hiddenPool := testPool(t)
			hiddenUOW := postgres.NewUnitOfWork(hiddenPool)
			seedRecommendationTenant(t, hiddenUOW, "t1")
			seedRecommendationTenant(t, hiddenUOW, "t2")
			if _, err := hiddenPool.Exec(ctx, setSellersModelKeys); err != nil {
				t.Fatal(err)
			}
			if _, err := hiddenPool.Exec(ctx, query); err != nil {
				t.Fatal(err)
			}
			sellers := postgres.NewSellersCounts(postgres.Single(hiddenPool))
			counts, err := sellers.CountSellers(ctx, []string{"model", "other"})
			if err != nil || counts["model"] != 1 || counts["other"] != 1 {
				t.Fatal(counts, err)
			}
		})
	}
}

func TestSellersCountsEmptyWithoutConnection(t *testing.T) {
	repository := &postgres.SellersCounts{}
	counts, err := repository.CountSellers(context.Background(), nil)
	if err != nil || len(counts) != 0 {
		t.Fatal(counts, err)
	}
}
