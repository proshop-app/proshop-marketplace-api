package catalog_test

import (
	"time"

	appcatalog "github.com/proshop-app/proshop-marketplace/internal/application/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	"github.com/proshop-app/proshop-marketplace/internal/domain/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
	"github.com/proshop-app/proshop-marketplace/pkg/money"
)

var (
	t0 = time.Date(2026, 10, 23, 10, 0, 0, 0, time.UTC)
	t1 = t0.Add(time.Minute)
	t2 = t0.Add(2 * time.Minute)
)

func snapshot(tenant ids.TenantID, product ids.ProductID, revision int64, at time.Time) catalog.Snapshot {
	return catalog.Snapshot{
		TenantID:  tenant,
		ProductID: product,
		Revision:  revision,
		At:        at,
		Card: catalog.Card{
			Marketplace: &taxonomy.Card{CategoryID: "c1", Published: true},
			Publication: taxonomy.Publication{
				Published:    true,
				Version:      1,
				CategoryID:   "c1",
				CategoryName: "Категория",
				CategoryPath: []string{"c1"},
			},
			Name:       "Товар " + string(product),
			Visible:    true,
			Tags:       []string{"tag-1", "free"},
			Categories: []catalog.Category{{ID: "c1", Name: "Категория"}},
		},
		Price:     money.FromKopecks(10000),
		Available: 3,
	}
}

func seedSeller(store *fake.Store, tenant ids.TenantID, status participant.TenantStatus, enabled bool) {
	store.SeedTaxonomy(taxonomy.Definition{
		Version:    1,
		Categories: []taxonomy.Category{{ID: "c1", Name: "Категория"}},
	})
	store.SeedTenant(participant.TenantState{
		TenantID:  tenant,
		Slug:      "slug-" + string(tenant),
		Status:    status,
		EventAt:   t0,
		Version:   1,
		UpdatedAt: t0,
	})
	store.SeedParticipant(participant.State{
		TenantID:         tenant,
		Slug:             "slug-" + string(tenant),
		Name:             "Магазин " + string(tenant),
		Status:           participant.StatusConnected,
		PaymentAccountID: "acc",
		Enabled:          enabled,
		ShopEnabled:      true,
		Version:          1,
		CreatedAt:        t0,
		UpdatedAt:        t0,
	})
}

func seedProduct(store *fake.Store, tenant ids.TenantID, product ids.ProductID) {
	p, err := catalog.NewProduct(snapshot(tenant, product, 1, t0), t0)
	if err != nil {
		panic(err)
	}

	store.SeedProduct(p.State())
}

func productEvent(eventID string, snapshot catalog.Snapshot) appcatalog.ProductEvent {
	return appcatalog.ProductEvent{EventID: eventID, Snapshot: snapshot}
}
