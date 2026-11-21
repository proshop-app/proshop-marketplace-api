package catalog

import (
	"context"
	"log/slog"
	"slices"
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
	ValueDetails []VariantValue `json:"valueDetails,omitempty"`
	Key          string         `json:"key"`
	Code         string         `json:"code"`
	Name         string         `json:"name"`
	Values       []string       `json:"values"`
}

// VariantValue несёт каноническое значение оси и данные витрины.
type VariantValue struct {
	Value string              `json:"value"`
	Meta  *taxonomy.ValueMeta `json:"meta,omitempty"`
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

// Variants загружает группу отдельно от карточки.
func (q *Queries) Variants(ctx context.Context, tenantID, groupID string) (*VariantBlock, error) {
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
	return variantBlock(tenantID, groupID, group, q.display.Load()), nil
}

func variantBlock(tenantID, groupID string, group ports.OfferGroup, view *taxonomyDisplay) *VariantBlock {
	offers := compatibleVariants(tenantID, groupID, group.Items)
	items := variantItems(offers)
	return &VariantBlock{
		GroupID:   groupID,
		Options:   variantOptions(offers, items, view),
		Items:     items,
		Truncated: group.Truncated,
	}
}

func compatibleVariants(tenantID, groupID string, items []ports.OfferDocument) []ports.OfferDocument {
	items = orderedVariants(items)
	out := make([]ports.OfferDocument, 0, len(items))
	var axes []catalog.VariantAxis
	for _, item := range items {
		if !item.Visible || item.TenantID != tenantID || item.VariantsID != groupID {
			continue
		}
		variant := item.Variants
		if variant == nil || len(variant.Axes) == 0 {
			slog.Warn("нет осей варианта", "tenantId", tenantID, "groupId", groupID, "offer", item.ID)
			continue
		}
		if axes == nil {
			axes = variant.Axes
		}
		if !variantKeysMatch(variant.Axes, axes) {
			slog.Warn("оси варианта отличаются", "tenantId", tenantID, "groupId", groupID, "offer", item.ID)
			continue
		}
		out = append(out, item)
	}
	return out
}

func orderedVariants(items []ports.OfferDocument) []ports.OfferDocument {
	items = slices.Clone(items)
	sort.SliceStable(items, func(i, j int) bool {
		a, b := variantPosition(items[i]), variantPosition(items[j])
		if a != b {
			return a < b
		}
		return items[i].ID < items[j].ID
	})
	return items
}

func variantItems(offers []ports.OfferDocument) []VariantItem {
	items := make([]VariantItem, 0, len(offers))
	for _, offer := range offers {
		attrs := make(map[string]string, len(offer.Variants.Axes))
		for _, axis := range offer.Variants.Axes {
			attrs[axis.Key] = axis.Value
		}
		items = append(items, VariantItem{
			ID:        offer.ID,
			Attrs:     attrs,
			Position:  offer.Variants.Position,
			Price:     offer.Price,
			BasePrice: offer.BasePrice,
			Available: offer.Available,
			InStock:   offer.InStock,
			Image:     offer.Image,
		})
	}
	return items
}

func variantOptions(
	offers []ports.OfferDocument,
	items []VariantItem,
	view *taxonomyDisplay,
) []VariantOption {
	options := []VariantOption{}
	if len(offers) == 0 {
		return options
	}
	for _, axis := range offers[0].Variants.Axes {
		option := VariantOption{
			Key:    axis.Key,
			Code:   axis.Code,
			Name:   axis.Name,
			Values: variantValues(items, axis.Key),
		}
		view.presentVariantOption(&option, offers)
		options = append(options, option)
	}
	return options
}

func variantValues(items []VariantItem, key string) []string {
	values := []string{}
	seen := map[string]bool{}
	for _, item := range items {
		if value := item.Attrs[key]; value != "" && !seen[value] {
			values = append(values, value)
			seen[value] = true
		}
	}
	return values
}

func variantPosition(item ports.OfferDocument) int {
	if item.Variants == nil {
		return 0
	}
	return item.Variants.Position
}

func variantKeysMatch(axes, expected []catalog.VariantAxis) bool {
	keys := make(map[string]bool, len(axes))
	for _, axis := range axes {
		keys[axis.Key] = true
	}
	if len(keys) != len(expected) {
		return false
	}
	for _, axis := range expected {
		if !keys[axis.Key] {
			return false
		}
	}
	return true
}
