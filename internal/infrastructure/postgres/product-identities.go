package postgres

import (
	"context"
	"encoding/json"

	"github.com/proshop-app/proshop-marketplace/internal/domain/catalog"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

const saveProductIdentities = `UPDATE catalog_products p SET
    card = p.card || jsonb_build_object('modelKey', input.model_key,
        'modelKeySource', input.source, 'variantKey', input.variant_key),
    version = input.version
    FROM jsonb_to_recordset($1::jsonb) AS input(
        tenant_id text, product_id text, origin bigint, version bigint,
        model_key text, source text, variant_key text)
    WHERE p.tenant_id = input.tenant_id AND p.product_id = input.product_id AND p.version = input.origin
    RETURNING p.tenant_id, p.product_id`

type productIdentity struct {
	TenantID   ids.TenantID  `json:"tenant_id"`
	ProductID  ids.ProductID `json:"product_id"`
	Origin     int64         `json:"origin"`
	Version    int64         `json:"version"`
	ModelKey   string        `json:"model_key"`
	Source     string        `json:"source"`
	VariantKey string        `json:"variant_key"`
}

// SaveIdentities не трогает источник карточки; отдельный CAS каждого товара изолирует конфликты пачки.
func (r *Products) SaveIdentities(ctx context.Context, products []*catalog.Product) ([]ids.OfferID, error) {
	result := []ids.OfferID{}
	if len(products) == 0 {
		return result, nil
	}
	values := make([]productIdentity, 0, len(products))
	byID := map[ids.OfferID]*catalog.Product{}
	for _, product := range products {
		p := product.State()
		key := ids.OfferID{Tenant: p.TenantID, Product: p.ProductID}
		byID[key] = product
		values = append(values, productIdentity{
			TenantID:   p.TenantID,
			ProductID:  p.ProductID,
			Origin:     product.OriginVersion(),
			Version:    p.Version,
			ModelKey:   p.Card.ModelKey,
			Source:     p.Card.ModelKeySource,
			VariantKey: p.Card.VariantKey,
		})
	}
	payload, err := json.Marshal(values)
	if err != nil {
		return nil, err
	}
	rows, err := rawSQL(ctx, r.db, saveProductIdentities, string(payload)).Rows()
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = rows.Close()
	}()
	for rows.Next() {
		var key ids.OfferID
		if err := rows.Scan(&key.Tenant, &key.Product); err != nil {
			return nil, err
		}
		result = append(result, key)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, key := range result {
		byID[key].MarkSaved()
	}
	return result, nil
}
