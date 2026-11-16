package catalog

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"sort"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/catalog"
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
	keys, err := variantOfferIDs(group.Items)
	if err != nil {
		return nil, err
	}
	key := priceProfileKey(contexts, ids.TenantID(tenantID))
	var publicStates map[ids.OfferID]ports.OfferState
	if key == "" && len(keys) > 0 {
		publicStates, err = q.variantPublicStates(ctx, keys)
		if err != nil {
			return nil, err
		}
	}
	group.Items, err = pricedVariants(group.Items, keys, key, publicStates)
	if err != nil {
		return nil, err
	}
	return variantBlock(tenantID, groupID, group), nil
}

func variantOfferIDs(items []ports.OfferDocument) ([]ids.OfferID, error) {
	keys := make([]ids.OfferID, len(items))
	for i, item := range items {
		key, err := ids.ParseOfferID(item.ID)
		if err != nil {
			return nil, err
		}
		keys[i] = key
	}
	return keys, nil
}

func (q *Queries) variantPublicStates(
	ctx context.Context,
	keys []ids.OfferID,
) (map[ids.OfferID]ports.OfferState, error) {
	var states []ports.OfferState
	err := q.uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		var err error
		states, err = tx.Offers().Batch(ctx, keys)
		return err
	})
	if err != nil {
		return nil, err
	}
	byID := make(map[ids.OfferID]ports.OfferState, len(states))
	for _, state := range states {
		if state.Product != nil {
			product := state.Product.State()
			byID[ids.OfferID{Tenant: product.TenantID, Product: product.ProductID}] = state
		}
	}
	return byID, nil
}

func pricedVariants(
	items []ports.OfferDocument,
	keys []ids.OfferID,
	key string,
	publicStates map[ids.OfferID]ports.OfferState,
) ([]ports.OfferDocument, error) {
	priced := make([]ports.OfferDocument, 0, len(items))
	for i, doc := range items {
		err := priceOffer(&doc, key, publicStates[keys[i]])
		if errors.Is(err, catalog.ErrOfferNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		priced = append(priced, doc)
	}
	return priced, nil
}

func variantBlock(tenantID, groupID string, group ports.OfferGroup) *VariantBlock {
	offers := compatibleVariants(tenantID, groupID, group.Items)
	items := variantItems(offers)
	return &VariantBlock{
		GroupID:   groupID,
		Options:   variantOptions(offers, items),
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

func variantOptions(offers []ports.OfferDocument, items []VariantItem) []VariantOption {
	options := []VariantOption{}
	if len(offers) == 0 {
		return options
	}
	for _, axis := range offers[0].Variants.Axes {
		options = append(options, VariantOption{
			Key:    axis.Key,
			Code:   axis.Code,
			Name:   axis.Name,
			Values: variantValues(items, axis.Key),
		})
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
