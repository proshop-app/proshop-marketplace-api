package promotion_test

import (
	"context"
	"testing"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	app "github.com/proshop-app/proshop-marketplace/internal/application/promotion"
	"github.com/proshop-app/proshop-marketplace/internal/domain/promotion"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

type promotionReads struct {
	ports.UnitOfWork
	batches int
	singles int
	size    int
}

type promotionReadTx struct {
	ports.Tx
	owner *promotionReads
}

type promotionReadOffers struct {
	ports.OfferStates
	owner *promotionReads
}

func (u *promotionReads) Do(ctx context.Context, run func(context.Context, ports.Tx) error) error {
	return u.UnitOfWork.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		return run(ctx, promotionReadTx{Tx: tx, owner: u})
	})
}

func (t promotionReadTx) Offers() ports.OfferStates {
	return promotionReadOffers{OfferStates: t.Tx.Offers(), owner: t.owner}
}

func (o promotionReadOffers) Batch(ctx context.Context, keys []ids.OfferID) ([]ports.OfferState, error) {
	o.owner.batches++
	o.owner.size = len(keys)
	return o.OfferStates.Batch(ctx, keys)
}

func (o promotionReadOffers) Offer(
	ctx context.Context,
	tenant ids.TenantID,
	id ids.ProductID,
) (ports.OfferState, error) {
	o.owner.singles++
	return o.OfferStates.Offer(ctx, tenant, id)
}

func TestSponsoredOnlyBatchesRequestedCategory(t *testing.T) {
	e := setup()
	campaign := e.create(t, "category_slot")
	campaign.Status = promotion.Active
	e.store.SeedPromotion(campaign)
	unrelated := campaign
	unrelated.ID = "ffffffff-ffff-ffff-ffff-ffffffffffff"
	unrelated.Target.CategoryID = "other"
	unrelated.Target.OfferID.Product = "unrelated"
	e.store.SeedPromotion(unrelated)
	e.clock.Advance(72 * time.Hour)
	reads := &promotionReads{UnitOfWork: e.store}
	service := app.NewService(reads, e.clock, &fake.IDs{}, e.index, app.Config{
		Location:    time.UTC,
		SearchDepth: 100,
		Random:      fake.Random{},
	})
	rows, err := service.Sponsored(context.Background(), ports.OfferQuery{CategoryID: "c1"})
	if err != nil || len(rows) != 1 || reads.batches != 1 || reads.size != 1 || reads.singles != 0 {
		t.Fatalf("rows=%v batches=%d singles=%d size=%d err=%v", rows, reads.batches, reads.singles, reads.size, err)
	}
}
