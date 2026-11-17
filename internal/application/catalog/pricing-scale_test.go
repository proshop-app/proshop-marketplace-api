package catalog_test

import (
	"context"
	"reflect"
	"testing"

	app "github.com/proshop-app/proshop-marketplace/internal/application/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

type pricingScaleStore struct {
	ports.UnitOfWork
	events *[]string
}

type pricingScaleTx struct {
	ports.Tx
	events *[]string
}

type pricingScaleParticipants struct {
	participant.Repository
	events *[]string
}

type pricingScaleCache struct {
	events   *[]string
	profiles map[ids.TenantID]string
}

func (s pricingScaleStore) Do(ctx context.Context, run func(context.Context, ports.Tx) error) error {
	return s.UnitOfWork.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		return run(ctx, pricingScaleTx{Tx: tx, events: s.events})
	})
}

func (s pricingScaleTx) Participants() participant.Repository {
	return pricingScaleParticipants{Repository: s.Tx.Participants(), events: s.events}
}

func (s pricingScaleParticipants) PublicTenants(
	ctx context.Context,
	filter []ids.TenantID,
	after ids.TenantID,
	limit int,
) ([]ids.TenantID, error) {
	*s.events = append(*s.events, "sql")
	return s.Repository.PublicTenants(ctx, filter, after, limit)
}

func (s pricingScaleCache) PriceContexts(
	_ context.Context,
	_ ids.UUID,
	_ []ids.TenantID,
) (map[ids.TenantID]string, error) {
	*s.events = append(*s.events, "cache")
	return s.profiles, nil
}

func (s pricingScaleCache) Preload(ids.UUID) {
	*s.events = append(*s.events, "preload")
}

func TestPriceContextsReadCacheBeforeTargetedVisibility(t *testing.T) {
	store := fake.NewStore()
	seedSeller(store, "t1", participant.TenantActive, true)
	seedSeller(store, "t2", participant.TenantActive, false)
	events := []string{}
	uow := pricingScaleStore{UnitOfWork: store, events: &events}
	cache := pricingScaleCache{
		events: &events,
		profiles: map[ids.TenantID]string{
			"t1": "marketplace:personal",
			"t2": "marketplace:personal",
		},
	}
	got, err := app.PriceContexts(context.Background(), uow, cache, "buyer", []ids.TenantID{"t1", "t2"})
	if err != nil || len(got) != 1 || got["t1"] != "marketplace:personal" {
		t.Fatalf("%v %v", got, err)
	}
	if !reflect.DeepEqual(events, []string{"cache", "sql"}) {
		t.Fatal(events)
	}
	events = nil
	got, err = app.PriceContexts(context.Background(), uow, cache, "buyer", []ids.TenantID{})
	if err != nil || len(got) != 0 || len(events) != 0 {
		t.Fatalf("%v %v %v", got, err, events)
	}
}
