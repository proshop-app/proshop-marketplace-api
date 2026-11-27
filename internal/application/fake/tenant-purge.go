package fake

import (
	"context"
	"slices"
	"strings"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

type tenantPurge struct {
	data *data
}

func (p tenantPurge) Available(_ context.Context, tenants []ids.TenantID) (map[ids.TenantID]bool, error) {
	result := make(map[ids.TenantID]bool, len(tenants))
	for _, tenant := range tenants {
		_, deleted := p.data.tenantPurges[tenant]
		result[tenant] = !deleted
	}
	return result, nil
}

func (p tenantPurge) Begin(_ context.Context, tenant ids.TenantID) (bool, error) {
	if _, exists := p.data.tenantPurges[tenant]; exists {
		return false, nil
	}
	p.data.tenantPurges[tenant] = ports.TenantPurgeCursor{}
	return true, nil
}

func (p tenantPurge) LockCursor(_ context.Context, tenant ids.TenantID) (ports.TenantPurgeCursor, error) {
	return p.data.tenantPurges[tenant], nil
}

func (p tenantPurge) IndexPage(
	_ context.Context, tenant ids.TenantID, step int64, limit int,
) ([]ids.ProductID, bool, error) {
	cursor, ok := p.data.tenantPurges[tenant]
	if !ok || cursor.Step != step || cursor.IndexDone {
		return nil, false, nil
	}
	var products []ids.ProductID
	for key := range p.data.products {
		if key.tenant == tenant && key.product > cursor.Product {
			products = append(products, key.product)
		}
	}
	slices.Sort(products)
	return products[:min(len(products), limit)], true, nil
}

func (p tenantPurge) DeletePage(_ context.Context, tenant ids.TenantID, limit int) (time.Time, error) {
	products := 0
	for key := range p.data.products {
		if key.tenant == tenant {
			if products == limit {
				return p.data.databaseNow(), nil
			}
			delete(p.data.products, key)
			products++
		}
	}
	for key := range p.data.signals {
		if key.tenant == tenant {
			delete(p.data.signals, key)
		}
	}
	p.data.outbox = slices.DeleteFunc(p.data.outbox, func(row OutboxRow) bool {
		return row.TenantID == tenant && row.Kind != ports.TaskPurgeTenant &&
			row.Kind != "search.purchase" && !strings.HasPrefix(row.Kind, "checkout.")
	})
	return time.Time{}, nil
}

func (p tenantPurge) Finish(_ context.Context, tenant ids.TenantID) error {
	delete(p.data.participants, tenant)
	delete(p.data.tenants, tenant)
	delete(p.data.storeCards, tenant)
	return nil
}

func (p tenantPurge) Advance(_ context.Context, tenant ids.TenantID, cursor ports.TenantPurgeCursor) error {
	p.data.tenantPurges[tenant] = cursor
	return nil
}
