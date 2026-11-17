package platformguard

import (
	"context"

	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

// PriceContexts ограничивает фоновый вызов; устаревшие данные никогда не выдаются как новая загрузка.
func (c *Client) PriceContexts(
	ctx context.Context,
	buyer ids.UUID,
	tenants []ids.TenantID,
) (map[ids.TenantID]string, error) {
	// В ошибке пачки нет tenantId: её нельзя начислять всем магазинам.
	// Единственный магазин известен точно и получает обычную изоляцию.
	if len(tenants) == 1 {
		ctx = context.WithValue(ctx, tenantContextKey{}, tenants[0])
	}
	load := func(ctx context.Context) (map[ids.TenantID]string, error) {
		return c.upstream.PriceContexts(ctx, buyer, tenants)
	}
	return call(ctx, c, "price_contexts", c.cfg.ReadTimeout, load)
}
