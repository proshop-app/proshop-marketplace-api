package postgres

import (
	"context"
	"fmt"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

const (
	tileOfferKeys = `
   SELECT p.tenant_id, p.tile_key
   FROM changed c JOIN catalog_products p USING (tenant_id, product_id)
   UNION
   SELECT * FROM unnest($3::text[], $4::text[])`
	selectTiles = `
 WITH changed AS MATERIALIZED (
   SELECT DISTINCT * FROM unnest($1::text[], $2::text[]) AS c(tenant_id, product_id)
 ),
 requested AS MATERIALIZED (` + tileOfferKeys + `)` + tileRows
	tilePageChanged = `
   AND (p.updated_at >= $4 OR pa.updated_at >= $4 OR t.updated_at >= $4
     OR o.updated_at >= $4 OR mv.updated_at >= $4)`
	tilePageSources = `
   SELECT p.tenant_id, p.tile_key
   FROM catalog_products p
   JOIN participants pa USING (tenant_id)
   LEFT JOIN tenants t USING (tenant_id)
   LEFT JOIN offers o USING (tenant_id, product_id)
   LEFT JOIN moderation_verdicts mv USING (tenant_id, product_id)
   WHERE (p.tenant_id, p.tile_key) > ($1, $2)
 AND NOT EXISTS (SELECT 1 FROM marketplace_tenant_deletions x WHERE x.tenant_id = p.tenant_id)`
	tilePageTail = `
   GROUP BY 1,2 ORDER BY 1,2 LIMIT $3
 ), changed AS (SELECT ''::text AS tenant_id, ''::text AS product_id WHERE false)`
	selectTilePage = `
 WITH requested AS MATERIALIZED (
   SELECT p.tenant_id,p.tile_key
   FROM catalog_products p JOIN participants pa USING (tenant_id)
   WHERE (p.tenant_id,p.tile_key) > ($1,$2)
     AND NOT EXISTS (SELECT 1 FROM marketplace_tenant_deletions x WHERE x.tenant_id = p.tenant_id)
 ` + tilePageTail + tileRowsPage
	selectTenantTilePage = `
 WITH requested AS MATERIALIZED (
   SELECT p.tenant_id,p.tile_key
   FROM catalog_products p JOIN participants pa USING (tenant_id)
   WHERE p.tenant_id = $1 AND p.tile_key > $2` + tilePageTail + tileRowsPage
	selectChangedTilePage = `
 WITH requested AS MATERIALIZED (
   SELECT tenant_id,tile_key FROM (
   (` + tilePageSources + tilePageChanged + `
     GROUP BY 1,2 ORDER BY 1,2 LIMIT $3)
   UNION ALL
   (SELECT d.tenant_id,d.tile_key
    FROM catalog_tile_departures d JOIN participants pa USING (tenant_id)
    WHERE (d.tenant_id,d.tile_key) > ($1,$2) AND d.updated_at >= $4
      AND NOT EXISTS (SELECT 1 FROM marketplace_tenant_deletions x WHERE x.tenant_id = d.tenant_id)
    ORDER BY 1,2 LIMIT $3)
   ) changed_keys
 ` + tilePageTail + tileRowsPage
	// Пустая плитка имеет строку метаданных с p.product_id NULL.
	tileProductColumns = `
   COALESCE(p.tenant_id, ''), COALESCE(p.product_id, ''), COALESCE(p.revision, 0),
   COALESCE(p.deleted, true), p.card, p.price, p.base_price, COALESCE(p.available, 0),
   COALESCE(p.projected_at, 'epoch'), COALESCE(p.version, 0), COALESCE(p.updated_at, 'epoch'),
   COALESCE(p.sales_count, 0), COALESCE(p.average_rating, 0), COALESCE(p.ratings_count, 0)`
	tileRows     = tileRowsHead + `c.product_id IS NOT NULL` + tileRowsTail
	tileRowsPage = tileRowsHead + `true` + tileRowsTail
	tileRowsHead = `
 SELECT ` + tileProductColumns + offerDetailsColumns + `,
   CASE WHEN c.product_id IS NULL THEN false ELSE EXISTS (
     SELECT 1 FROM store_summary_changes s WHERE s.tenant_id = p.tenant_id AND s.product_id = p.product_id
       AND s.batch_no IS NOT NULL AND NOT s.scheduled
   ) END,
   k.tenant_id, k.tile_key, COALESCE(d.version, 0) + COALESCE(pa.version, 0) + COALESCE(t.version, 0),
   COALESCE(d.updated_at, 'epoch'),
   COALESCE(p.version,0) + COALESCE(o.version,0) + COALESCE(mv.version,0), `
	// LATERAL сохраняет точечные проходы по составному индексу для каждого ключа пачки.
	tileRowsTail = `
 FROM requested k
 LEFT JOIN LATERAL (
   SELECT p.* FROM catalog_products p WHERE p.tenant_id = k.tenant_id AND p.tile_key = k.tile_key
   OFFSET 0
 ) p ON true
 LEFT JOIN changed c ON c.tenant_id = p.tenant_id AND c.product_id = p.product_id
 LEFT JOIN offers o ON o.tenant_id = p.tenant_id AND o.product_id = p.product_id
 LEFT JOIN participants pa ON pa.tenant_id = k.tenant_id
 LEFT JOIN tenants t ON t.tenant_id = k.tenant_id
 LEFT JOIN moderation_verdicts mv ON mv.tenant_id = p.tenant_id AND mv.product_id = p.product_id
 LEFT JOIN catalog_tile_departures d ON d.tenant_id = k.tenant_id AND d.tile_key = k.tile_key
 WHERE pa.tenant_id IS NOT NULL
 AND NOT EXISTS (SELECT 1 FROM marketplace_tenant_deletions x WHERE x.tenant_id = k.tenant_id)
 ORDER BY k.tenant_id, k.tile_key, p.product_id`
)

// Tiles читает состав затронутых плиток одним снимком.
func (r *Offers) Tiles(ctx context.Context, offers []ids.OfferID, former []ports.TileKey) ([]ports.TileState, error) {
	tenants, products := make([]string, 0, len(offers)), make([]string, 0, len(offers))
	formerTenants, keys := make([]string, 0, len(former)), make([]string, 0, len(former))
	for _, offer := range offers {
		tenants = append(tenants, string(offer.Tenant))
		products = append(products, string(offer.Product))
	}
	for _, tile := range former {
		formerTenants = append(formerTenants, string(tile.TenantID))
		keys = append(keys, tile.Key)
	}
	return r.tileRows(ctx, selectTiles, tenants, products, formerTenants, keys)
}

// TilePage читает следующую порцию ключей вместе с их полным составом.
func (r *Offers) TilePage(ctx context.Context, page ports.TilePage) ([]ports.TileState, error) {
	switch {
	case page.TenantID != "":
		return r.tileRows(ctx, selectTenantTilePage, string(page.TenantID), page.After.Key, page.Limit)
	case !page.Since.IsZero():
		tenant := string(page.After.TenantID)
		return r.tileRows(ctx, selectChangedTilePage, tenant, page.After.Key, page.Limit, page.Since)
	default:
		return r.tileRows(ctx, selectTilePage, string(page.After.TenantID), page.After.Key, page.Limit)
	}
}

func (r *Offers) tileRows(ctx context.Context, query string, args ...any) ([]ports.TileState, error) {
	rows, err := rawSQL(ctx, r.db, query, args...).Rows()
	if err != nil {
		return nil, fmt.Errorf("снимки плиток: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var tiles []ports.TileState
	for rows.Next() {
		var row offerRow
		var tile ports.TileState
		var memberVersion int64
		var changed bool
		targets := append(
			row.targets(),
			&tile.Key.TenantID,
			&tile.Key.Key,
			&tile.Version,
			&tile.UpdatedAt,
			&memberVersion,
			&changed,
		)
		if err := rows.Scan(targets...); err != nil {
			return nil, fmt.Errorf("строка плитки: %w", err)
		}
		if len(tiles) == 0 || tiles[len(tiles)-1].Key != tile.Key {
			tiles = append(tiles, tile)
		}
		if row.product.productID == "" || row.participant.tenantID == nil {
			continue
		}
		state, err := row.state()
		if err != nil {
			return nil, err
		}
		current := &tiles[len(tiles)-1]
		current.Version += memberVersion
		current.Members = append(current.Members, ports.TileMember{State: state, Changed: changed})
	}
	return tiles, rows.Err()
}
