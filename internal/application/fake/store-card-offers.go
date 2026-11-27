package fake

import (
	"cmp"
	"context"
	"slices"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/domain/moderation"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

type summaryRank struct {
	at       time.Time
	revision int64
}

type summaryOffers []catalog.ProductState

// Rollup пересобирает эталонную сводку из памяти для тестов сценариев.
func (r storeCards) Rollup(
	_ context.Context,
	tenant ids.TenantID,
	public bool,
	categories []string,
) (ports.StoreSummaryFacts, error) {
	candidates, visible := summaryOffers{}, summaryOffers{}
	known := make(map[string]bool, len(categories))
	for _, category := range categories {
		known[category] = true
	}
	result := ports.StoreSummaryFacts{
		Categories: map[string]int{},
		Products:   []ports.ShopCardProduct{},
	}
	for key, p := range r.d.products {
		if key.tenant != tenant {
			continue
		}
		delete(r.d.storeSummaryPending, key)
		if p.Deleted {
			continue
		}
		if raw := p.Card.Marketplace; raw != nil && raw.Published && !known[raw.CategoryID] {
			result.Uncategorized++
		}
		if !p.HasPrice || !p.Card.Publication.Published {
			continue
		}
		candidates = append(candidates, p)
		if public && p.Card.Visible && r.d.moderation.verdicts[key].Status == moderation.Allowed {
			visible = append(visible, p)
		}
	}
	result.VisibleOffers = len(visible)
	selected := visible
	if len(selected) == 0 {
		selected = candidates
	}
	result.Offers = len(selected)
	for _, p := range selected {
		price := p.Price.Kopecks()
		if result.FromPrice == nil || price < *result.FromPrice {
			result.FromPrice = &price
		}
		if id := p.Card.Publication.CategoryID; known[id] {
			result.Categories[id]++
		}
	}
	result.Rating = visible.rating()
	result.Products = r.storeProducts(visible)
	return result, nil
}

func (r storeCards) storeProducts(products summaryOffers) []ports.ShopCardProduct {
	slices.SortFunc(products, func(a, b catalog.ProductState) int {
		aRank := r.d.storeRanked[productKey{a.TenantID, a.ProductID}]
		bRank := r.d.storeRanked[productKey{b.TenantID, b.ProductID}]
		return cmp.Or(
			cmp.Compare(b.SalesCount, a.SalesCount),
			cmp.Compare(summaryHasImage(b), summaryHasImage(a)),
			bRank.at.Compare(aRank.at),
			cmp.Compare(bRank.revision, aRank.revision),
			cmp.Compare(a.ProductID, b.ProductID),
		)
	})
	result := make([]ports.ShopCardProduct, 0, min(len(products), 3))
	for _, p := range products[:min(len(products), 3)] {
		var image *string
		if p.Card.Image != "" {
			image = &p.Card.Image
		}
		result = append(result, ports.ShopCardProduct{
			ProductID: string(p.ProductID),
			OfferID:   ids.OfferID{Tenant: p.TenantID, Product: p.ProductID}.String(),
			Name:      p.Card.Name,
			Image:     image,
		})
	}
	return result
}

func summaryHasImage(p catalog.ProductState) int {
	if p.Card.Image != "" {
		return 1
	}
	return 0
}

func (products summaryOffers) rating() *catalog.Rating {
	first := map[string]catalog.ProductState{}
	for _, product := range products {
		group := catalog.RatingGroup(product)
		if current, exists := first[group]; !exists || product.ProductID < current.ProductID {
			first[group] = product
		}
	}
	var weight float64
	var ratings int64
	for _, p := range first {
		weight += p.AverageRating * float64(p.RatingsCount)
		ratings += p.RatingsCount
	}
	return catalog.ProductRating(weight/float64(ratings), ratings)
}
