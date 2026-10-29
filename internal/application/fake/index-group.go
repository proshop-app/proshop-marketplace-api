package fake

import (
	"context"
	"sort"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
)

const maxVariantGroup = 200

// Group возвращает видимые позиции группы одного продавца.
func (x *Index) Group(_ context.Context, tenantID, groupID string) (ports.OfferGroup, error) {
	x.mu.Lock()
	defer x.mu.Unlock()
	page := ports.OfferGroup{
		Items: []ports.OfferDocument{},
	}
	for _, doc := range x.generations[x.active] {
		if doc.TenantID == tenantID && doc.VariantsID == groupID && doc.Visible {
			page.Items = append(page.Items, doc)
		}
	}
	sort.Slice(page.Items, func(i, j int) bool {
		a, b := page.Items[i], page.Items[j]
		if a.Variants != nil && b.Variants != nil && a.Variants.Position != b.Variants.Position {
			return a.Variants.Position < b.Variants.Position
		}
		return a.ID < b.ID
	})
	if len(page.Items) > maxVariantGroup {
		page.Truncated = true
		page.Items = page.Items[:maxVariantGroup]
	}

	return page, nil
}
