package catalog

import (
	"context"
	"errors"
	"sort"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

// VariantBlock содержит всю доступную часть группы продавца.
type VariantBlock struct {
	GroupID   string          `json:"groupId"`
	Options   []VariantOption `json:"options"`
	Items     []VariantItem   `json:"items"`
	Truncated bool            `json:"truncated"`
}

// VariantOption содержит метаданные оси и только существующие значения.
type VariantOption struct {
	Key    string   `json:"key"`
	Code   string   `json:"code"`
	Name   string   `json:"name"`
	Values []string `json:"values"`
}

// VariantItem — предложение со значениями всех осей и выбранной ценой.
type VariantItem struct {
	ID        string            `json:"id"`
	Attrs     map[string]string `json:"attrs"`
	Position  int               `json:"position"`
	Price     int64             `json:"price"`
	BasePrice int64             `json:"basePrice"`
	Available int               `json:"available"`
	InStock   bool              `json:"inStock"`
	Image     string            `json:"image,omitempty"`
}

// Variants загружает группу отдельно от карточки с контекстом покупателя.
func (q *Queries) Variants(ctx context.Context, tenantID, groupID string, buyerID ids.UUID) (*VariantBlock, error) {
	if _, err := ids.ParseTenantID(tenantID); err != nil {
		return nil, err
	}
	if _, err := ids.ParseProductID(groupID); err != nil {
		return nil, err
	}
	group, err := q.index.Group(ctx, tenantID, groupID)
	if err != nil {
		return nil, err
	}
	contexts, err := q.priceContexts(ctx, buyerID, tenantID)
	if err != nil {
		return nil, err
	}
	items := make([]ports.OfferDocument, 0, len(group.Items))
	for _, doc := range group.Items {
		if !doc.Visible || doc.TenantID != tenantID || doc.VariantsID != groupID {
			continue
		}
		id, err := ids.ParseOfferID(doc.ID)
		if err != nil {
			return nil, err
		}
		err = q.priceOffer(ctx, id, &doc, buyerID, contexts)
		if errors.Is(err, catalog.ErrOfferNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		items = append(items, doc)
	}
	group.Items = items
	var definition taxonomy.Definition
	err = q.uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		var err error
		definition, err = tx.Taxonomy().Current(ctx)
		return err
	})
	if err != nil {
		return nil, err
	}
	return variantBlock(tenantID, groupID, group, definition), nil
}

func variantBlock(
	tenantID, groupID string,
	group ports.OfferGroup,
	definition taxonomy.Definition,
) *VariantBlock {
	block := &VariantBlock{
		GroupID:   groupID,
		Options:   []VariantOption{},
		Items:     []VariantItem{},
		Truncated: group.Truncated,
	}
	var category taxonomy.CategorySchema
	for _, item := range group.Items {
		if !item.Visible || item.TenantID != tenantID || item.VariantsID != groupID {
			continue
		}
		variant := item.Variants
		if variant == nil {
			variant = &catalog.Variants{}
		}
		if len(block.Options) == 0 {
			category, _, _ = definition.Category(item.CategoryID)
			for _, axis := range variant.Axes {
				block.Options = append(block.Options, VariantOption{
					Key:    axis.Key,
					Code:   axis.Code,
					Name:   axis.Name,
					Values: []string{},
				})
			}
		}
		attrs := make(map[string]string, len(variant.Axes))
		for _, axis := range variant.Axes {
			attrs[axis.Key] = axis.Value
		}
		block.Items = append(block.Items, VariantItem{
			ID:        item.ID,
			Attrs:     attrs,
			Position:  variant.Position,
			Price:     item.Price,
			BasePrice: item.BasePrice,
			Available: item.Available,
			InStock:   item.InStock,
			Image:     item.Image,
		})
	}
	sort.SliceStable(block.Items, func(i, j int) bool {
		a, b := block.Items[i], block.Items[j]
		if a.Position != b.Position {
			return a.Position < b.Position
		}
		return a.ID < b.ID
	})
	for i := range block.Options {
		option := &block.Options[i]
		seen := map[string]bool{}
		for _, item := range block.Items {
			if value := item.Attrs[option.Key]; value != "" && !seen[value] {
				option.Values = append(option.Values, value)
				seen[value] = true
			}
		}
		order := map[string]int{}
		for _, attribute := range category.Attributes {
			if attribute.Code == option.Code {
				for index, value := range attribute.Values {
					order[value] = index + 1
				}
			}
		}
		sort.SliceStable(option.Values, func(i, j int) bool {
			a, b := order[option.Values[i]], order[option.Values[j]]
			return a != 0 && (b == 0 || a < b)
		})
	}
	return block
}
