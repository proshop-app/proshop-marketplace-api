package participant

import (
	"context"
	"sort"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
)

func summarizeStore(
	ctx context.Context,
	tx ports.Tx,
	p *participant.Participant,
	visible bool,
) (StoreSummary, bool, error) {
	definition, err := tx.Taxonomy().Current(ctx)
	if err != nil {
		return StoreSummary{}, false, err
	}
	paths := categoryPaths(definition.Categories)
	categories := make([]string, 0, len(paths)+1)
	for id := range paths {
		categories = append(categories, id)
	}
	facts, err := tx.StoreCards().Rollup(ctx, p.TenantID(), visible, categories)
	if err != nil {
		return StoreSummary{}, false, err
	}
	if facts.More {
		return StoreSummary{}, true, nil
	}
	result := StoreSummary{
		CacheSignature: facts.CacheSignature,
		Products:       facts.Products,
		VisibleOffers:  facts.VisibleOffers,
		CategoryCounts: map[string]StoreCategory{},
		Offers:         facts.Offers,
		FromPrice:      facts.FromPrice,
		Categories:     []StoreCategory{},
		Uncategorized:  facts.Uncategorized,
		Hidden:         append([]participant.CardHidden{}, p.State().StoreCard.Hidden...),
		Rating:         facts.Rating,
	}
	for id, count := range facts.Categories {
		for _, category := range paths[id] {
			category.Count = result.CategoryCounts[category.ID].Count + count
			result.CategoryCounts[category.ID] = category
		}
	}
	for id, category := range result.CategoryCounts {
		if len(paths[id]) == 1 {
			result.Categories = append(result.Categories, category)
		}
	}
	sort.Slice(result.Categories, func(i, j int) bool {
		a, b := result.Categories[i], result.Categories[j]
		if a.Count != b.Count {
			return a.Count > b.Count
		}
		if a.Name != b.Name {
			return a.Name < b.Name
		}
		return a.ID < b.ID
	})
	return result, false, nil
}
