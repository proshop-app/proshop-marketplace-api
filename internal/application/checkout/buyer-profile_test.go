package checkout_test

import (
	"context"
	"errors"
	"testing"

	appbuyer "github.com/proshop-app/proshop-marketplace/internal/application/buyer"
	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

func TestPickupAndProfileCommitTogether(t *testing.T) {
	e := setup(t)
	for _, id := range []string{"t1", "t2"} {
		e.store.SeedTenant(participant.TenantState{TenantID: ids.TenantID(id), Status: participant.TenantActive})
	}
	ctx := context.Background()
	created, err := e.service.Create(ctx, buyerID, key)
	if err != nil {
		t.Fatal(err)
	}
	failure := errors.New("отказ фиксации")
	e.store.FailCommit = failure
	if _, err := e.service.Quote(ctx, buyerID, created.ID, "cdek", "MSK1"); !errors.Is(err, failure) {
		t.Fatal(err)
	}
	e.store.FailCommit = nil
	profiles := appbuyer.NewFeatures(e.store, nil, fake.NewPlatform(), fake.NewLimiter())
	profile, err := profiles.Profile(ctx, buyerID)
	if err != nil || profile.PickupPoint != nil {
		t.Fatal(profile, err)
	}
	state, ok := e.store.Checkout(created.ID)
	if !ok || state.Delivery != nil {
		t.Fatal("оформление не откатилось")
	}
	if _, err := e.service.Quote(ctx, buyerID, created.ID, "cdek", "MSK1"); err != nil {
		t.Fatal(err)
	}
	profile, err = profiles.Profile(ctx, buyerID)
	if err != nil || profile.PickupPoint == nil || profile.PickupPoint.Code != "MSK1" {
		t.Fatal(profile, err)
	}
}
