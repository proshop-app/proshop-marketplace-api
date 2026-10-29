package catalog

import (
	"context"
	"log/slog"
	"sort"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/catalog"
)

// VariantBlock содержит варианты текущего продавца.
type VariantBlock struct {
	GroupID   string              `json:"groupId"`
	Axis      catalog.VariantAxis `json:"axis"`
	Items     []VariantItem       `json:"items"`
	Truncated bool                `json:"truncated"`
}

// VariantItem описывает доступную покупателю позицию группы.
type VariantItem struct {
	ID        string `json:"id"`
	Current   bool   `json:"current"`
	Value     string `json:"value"`
	Position  int    `json:"position"`
	Price     int64  `json:"price"`
	BasePrice int64  `json:"basePrice"`
	Available int    `json:"available"`
	InStock   bool   `json:"inStock"`
	Image     string `json:"image,omitempty"`
	Duplicate bool   `json:"duplicate,omitempty"`
}

func (q *Queries) offerVariants(
	ctx context.Context,
	current ports.OfferDocument,
	priceKey string,
) (*VariantBlock, error) {
	if current.Variants == nil {
		return nil, nil
	}
	group, err := q.index.Group(ctx, current.TenantID, current.Variants.GroupID)
	if err != nil {
		return nil, err
	}
	var values []string
	err = q.uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		definition, err := tx.Taxonomy().Current(ctx)
		if err != nil {
			return err
		}
		category, _, found := definition.Category(current.CategoryID)
		if found {
			for _, attribute := range category.Attributes {
				if attribute.Code == current.Variants.Axis.Code {
					values = attribute.Values
					break
				}
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	return variantBlock(current, group, priceKey, values), nil
}

func variantBlock(current ports.OfferDocument, group ports.OfferGroup, priceKey string, values []string) *VariantBlock {
	block := &VariantBlock{
		GroupID:   current.Variants.GroupID,
		Axis:      current.Variants.Axis,
		Items:     []VariantItem{},
		Truncated: group.Truncated,
	}
	currentFound := false
	for _, item := range group.Items {
		if item.ID == current.ID {
			currentFound = true
		}
	}
	// Карточка за пределами первых 200 тоже должна иметь выбранный пункт.
	if !currentFound {
		if len(group.Items) >= 200 {
			group.Items = group.Items[:199]
			block.Truncated = true
		}
		group.Items = append(group.Items, current)
	}
	for _, item := range group.Items {
		variant := item.Variants
		if !item.Visible || item.TenantID != current.TenantID || variant == nil || variant.GroupID != block.GroupID {
			continue
		}
		if variant.Axis.Code != block.Axis.Code || variant.Axis.Key != block.Axis.Key {
			slog.Warn("ось варианта отличается", "tenantId", item.TenantID, "groupId", block.GroupID, "offer", item.ID)
			continue
		}
		profile, found := catalog.SelectPrice(item.PriceProfiles, priceKey)
		if !found {
			continue
		}
		block.Items = append(block.Items, VariantItem{
			ID:        item.ID,
			Current:   item.ID == current.ID,
			Value:     variant.Value,
			Position:  variant.Position,
			Price:     profile.EffectivePrice.Kopecks(),
			BasePrice: profile.Price.Kopecks(),
			Available: item.Available,
			InStock:   item.InStock,
			Image:     item.Image,
		})
	}
	order := make(map[string]int, len(values))
	for i, value := range values {
		order[value] = i + 1
	}
	sort.SliceStable(block.Items, func(i, j int) bool {
		a, b := block.Items[i], block.Items[j]
		ai, bi := order[a.Value], order[b.Value]
		if ai != bi {
			if ai == 0 {
				return false
			}
			if bi == 0 {
				return true
			}
			return ai < bi
		}
		if a.Position != b.Position {
			return a.Position < b.Position
		}
		return a.ID < b.ID
	})
	seen := map[string]bool{}
	for i := range block.Items {
		item := &block.Items[i]
		item.Duplicate = seen[item.Value]
		seen[item.Value] = true
	}
	if len(block.Items) < 2 {
		return nil
	}

	return block
}
