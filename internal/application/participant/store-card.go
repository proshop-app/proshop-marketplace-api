package participant

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

const (
	storeDefaultLimit = 20
	storeMaxLimit     = 100
	storeBatchSize    = 200
	storeProductsSize = 3
)

// StoreCategory — счётчик корневой категории магазина.
type StoreCategory = ports.StoreCategory

// StoreSummary — внутренняя статистика магазина.
type StoreSummary = ports.StoreSummary

// ShopHeader — публичный профиль с учётом постмодерации.
type ShopHeader = ports.ShopHeader

// ShopCategory — категория отбора либо крупнейшая категория магазина.
type ShopCategory = ports.ShopCategory

// ShopCard — карточка магазина в публичном списке.
type ShopCard = ports.ShopCard

// ShopPage — страница магазинов с общим числом результатов.
type ShopPage = ports.ShopPage

type storeOffer struct {
	product catalog.ProductState
	offer   catalog.Offer
	root    StoreCategory
}

type storeProductSignature struct {
	Product    ports.StoreCardProduct
	CategoryID string
}

// StoreSummary читает статистику даже для ещё не подключённого кандидата.
func (s *Service) StoreSummary(ctx context.Context, tenant ids.TenantID) (StoreSummary, error) {
	var summary StoreSummary
	err := s.uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		record, err := tx.StoreCards().Get(ctx, tenant)
		if errors.Is(err, participant.ErrNotFound) {
			p, participantErr := tx.Participants().Get(ctx, tenant)
			if participantErr != nil {
				return participantErr
			}
			summary = StoreSummary{
				Categories:       []StoreCategory{},
				Products:         []ports.StoreCardProduct{},
				SelectedProducts: []ports.StoreCardProduct{},
				Hidden:           append([]participant.CardHidden{}, p.State().StoreCard.Hidden...),
			}
			return nil
		}
		summary = record.Summary
		return err
	})
	return summary, err
}

func categoryRoots(nodes []taxonomy.Category) map[string]StoreCategory {
	result := map[string]StoreCategory{}
	var visit func(taxonomy.Category, StoreCategory)
	visit = func(n taxonomy.Category, root StoreCategory) {
		result[n.ID] = root
		for _, child := range n.Children {
			visit(child, root)
		}
	}
	for _, n := range nodes {
		visit(n, StoreCategory{ID: n.ID, Name: n.Name})
	}
	return result
}

func summarizeStore(ctx context.Context, tx ports.Tx, p *participant.Participant) (StoreSummary, error) {
	result := StoreSummary{
		Categories:       []StoreCategory{},
		Products:         []ports.StoreCardProduct{},
		SelectedProducts: []ports.StoreCardProduct{},
		Hidden:           append([]participant.CardHidden{}, p.State().StoreCard.Hidden...),
	}
	definition, err := tx.Taxonomy().Current(ctx)
	if err != nil {
		return result, err
	}
	roots := categoryRoots(definition.Categories)
	paths := categoryPaths(definition.Categories)
	result.CategoryCounts = map[string]StoreCategory{}
	visible, candidates, uncategorized, err := storeOffers(ctx, tx, p, roots)
	if err != nil {
		return result, err
	}
	result.Uncategorized = uncategorized
	var weighted float64
	var ratings int64
	groups := map[string]bool{}
	for _, item := range visible {
		key := "product:" + string(item.product.ProductID)
		if item.product.Card.VariantsID != "" {
			key = "variants:" + item.product.Card.VariantsID
		}
		if groups[key] {
			continue
		}
		groups[key] = true
		weighted += item.product.AverageRating * float64(item.product.RatingsCount)
		ratings += item.product.RatingsCount
	}
	result.Rating = catalog.ProductRating(weighted/float64(ratings), ratings)
	result.VisibleOffers = len(visible)
	selected := visible
	if len(selected) == 0 {
		selected = candidates
	}
	counts := map[string]StoreCategory{}
	for _, item := range selected {
		result.Offers++
		for _, c := range paths[item.product.Card.Publication.CategoryID] {
			c.Count = result.CategoryCounts[c.ID].Count + 1
			result.CategoryCounts[c.ID] = c
		}
		price := item.offer.Price.Kopecks()
		if result.FromPrice == nil || price < *result.FromPrice {
			result.FromPrice = &price
		}
		if item.root.ID != "" {
			c := counts[item.root.ID]
			c.ID, c.Name = item.root.ID, item.root.Name
			c.Count++
			counts[c.ID] = c
		}
	}
	for _, c := range counts {
		result.Categories = append(result.Categories, c)
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
	result.Products = automaticStoreProducts(visible)
	result.SelectedProducts = pinnedStoreProducts(visible, p.State().StoreCard.ProductSettings().ProductIDs)
	result.CacheSignature, err = storeCardSignature(visible, p.State().StoreCard.ProductSettings())
	return result, err
}

// storeOffers отделяет публичные предложения от кандидатов, используемых только в статистике.
func storeOffers(
	ctx context.Context,
	tx ports.Tx,
	p *participant.Participant,
	roots map[string]StoreCategory,
) ([]storeOffer, []storeOffer, int, error) {
	var visible, candidates []storeOffer
	var uncategorized int
	for after := ids.ProductID(""); ; {
		states, err := tx.Offers().StoreCardOffers(ctx, p.TenantID(), after, storeBatchSize)
		if err != nil {
			return nil, nil, 0, err
		}
		for _, state := range states {
			product := state.Product.State()
			after = product.ProductID
			if product.Deleted || product.TenantID != p.TenantID() {
				continue
			}
			card := product.Card
			if card.Marketplace != nil && card.Marketplace.Published {
				if _, ok := roots[card.Marketplace.CategoryID]; !ok {
					uncategorized++
				}
			}
			visibility := participant.VisibilityOf(p, state.Tenant)
			seller := catalog.Seller{
				Slug:              p.Slug(),
				Name:              p.Name(),
				Visible:           visibility.Visible,
				CheckoutAvailable: visibility.CheckoutAvailable,
			}
			offer := catalog.ComposeOffer(state.Product, state.Signals, seller, state.Verdict)
			if _, ok := catalog.SelectPrice(product.PriceProfiles, catalog.DefaultPriceProfile); !ok {
				continue
			}
			item := storeOffer{product: product, offer: offer, root: roots[card.Publication.CategoryID]}
			if offer.Visible {
				visible = append(visible, item)
			}
			if card.Publication.Published {
				candidates = append(candidates, item)
			}
		}
		if len(states) < storeBatchSize {
			break
		}
	}
	return visible, candidates, uncategorized, nil
}

func automaticStoreProducts(items []storeOffer) []ports.StoreCardProduct {
	sortStoreOffers(items)
	products := []ports.StoreCardProduct{}
	for _, item := range items {
		if len(products) == storeProductsSize {
			break
		}
		products = append(products, storeProduct(item))
	}
	return products
}

func sortStoreOffers(items []storeOffer) {
	sort.Slice(items, func(i, j int) bool {
		a, b := items[i].product, items[j].product
		if a.SalesCount != b.SalesCount {
			return a.SalesCount > b.SalesCount
		}
		if (a.Card.Image != "") != (b.Card.Image != "") {
			return a.Card.Image != ""
		}
		if !a.ProjectedAt.Equal(b.ProjectedAt) {
			return a.ProjectedAt.After(b.ProjectedAt)
		}
		if a.Revision != b.Revision {
			return a.Revision > b.Revision
		}
		return a.ProductID < b.ProductID
	})
}

func pinnedStoreProducts(items []storeOffer, ids []string) []ports.StoreCardProduct {
	products := []ports.StoreCardProduct{}
	used := map[string]bool{}
	for _, id := range ids {
		if used[id] || len(products) == storeProductsSize {
			continue
		}
		for _, item := range items {
			if string(item.product.ProductID) == id {
				products = append(products, storeProduct(item))
				used[id] = true
				break
			}
		}
	}
	return products
}

func storeProduct(item storeOffer) ports.StoreCardProduct {
	var image *string
	if item.product.Card.Image != "" {
		value := item.product.Card.Image
		image = &value
	}
	return ports.StoreCardProduct{
		ProductID: string(item.product.ProductID),
		OfferID:   item.offer.ID.String(),
		Name:      item.product.Card.Name,
		Image:     image,
	}
}

func selectedStoreProducts(summary StoreSummary, enabled bool) []ports.StoreCardProduct {
	result := []ports.StoreCardProduct{}
	if !enabled {
		return result
	}
	used := map[string]bool{}
	for _, product := range append(summary.SelectedProducts, summary.Products...) {
		if used[product.ProductID] || len(result) == storeProductsSize {
			continue
		}
		result = append(result, product)
		used[product.ProductID] = true
	}
	return result
}

// Ранги и цены исключены: они обновляются по TTL, а состав публичных товаров — немедленно.
func storeCardSignature(items []storeOffer, settings participant.CardProducts) (string, error) {
	products := make([]storeProductSignature, 0, len(items))
	for _, item := range items {
		products = append(products, storeProductSignature{
			Product:    storeProduct(item),
			CategoryID: item.product.Card.Publication.CategoryID,
		})
	}
	sort.Slice(products, func(i, j int) bool {
		return products[i].Product.ProductID < products[j].Product.ProductID
	})
	value := struct {
		Products []storeProductSignature
		Settings participant.CardProducts
	}{Products: products, Settings: settings}
	raw, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", sha256.Sum256(raw)), nil
}

func shopHeader(p *participant.Participant, summary StoreSummary) ShopHeader {
	profile := p.PublicProfile()
	appearance, err := profile.Appearance.Normalize()
	if err != nil {
		appearance = participant.DefaultCardAppearance()
	}
	return ShopHeader{
		Rating:     summary.Rating,
		TenantID:   p.TenantID(),
		Slug:       p.Slug(),
		Name:       p.Name(),
		Logo:       profile.Logo,
		Appearance: appearance,
		Offers:     summary.Offers,
		Categories: summary.Categories,
		IsNew:      true,
	}
}

// RefreshStoreCard пересчитывает сводку после изменений источника.
func (s *Service) RefreshStoreCard(ctx context.Context, task ports.Task) error {
	if task.Kind != ports.TaskStoreCard {
		return ErrInvalidTask
	}
	return s.uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		if err := tx.StoreCards().Lock(ctx, task.TenantID); err != nil {
			return err
		}
		p, err := tx.Participants().Get(ctx, task.TenantID)
		if errors.Is(err, participant.ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		summary, err := summarizeStore(ctx, tx, p)
		if err != nil {
			return err
		}
		tenant, err := tx.Registry().Get(ctx, task.TenantID)
		if err != nil {
			return err
		}
		h := shopHeader(p, summary)
		card := ShopCard{
			Products:       selectedStoreProducts(summary, p.State().StoreCard.ProductSettings().Enabled),
			Rating:         h.Rating,
			TenantID:       h.TenantID,
			Slug:           h.Slug,
			Name:           h.Name,
			Logo:           h.Logo,
			Appearance:     h.Appearance,
			Offers:         h.Offers,
			FromPrice:      summary.FromPrice,
			MoreCategories: max(len(h.Categories)-1, 0),
			IsNew:          h.IsNew,
		}
		if len(h.Categories) > 0 {
			c := h.Categories[0]
			card.Category = &ShopCategory{ID: c.ID, Name: c.Name}
		}
		pagePublic := participant.VisibilityOf(p, tenant).Visible && summary.VisibleOffers > 0
		return tx.StoreCards().Save(ctx, ports.StoreRecord{
			ProfileVersion: p.Version(),
			CacheSignature: summary.CacheSignature,
			Summary:        summary,
			Header:         h,
			Card:           card,
			Public:         p.CardReady() && pagePublic,
			PagePublic:     pagePublic,
		})
	})
}

// Shop читает готовую публичную шапку.
func (s *Service) Shop(ctx context.Context, slug string) (ShopHeader, error) {
	var result ShopHeader
	err := s.uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		var err error
		result, err = tx.StoreCards().Shop(ctx, slug)
		return err
	})
	return result, err
}

// Shops читает страницу готовых сводок, отбор и сортировка выполняются хранилищем.
func (s *Service) Shops(ctx context.Context, category string, limit, offset int) (ShopPage, error) {
	if limit <= 0 {
		limit = storeDefaultLimit
	}
	limit = min(limit, storeMaxLimit)
	offset = max(offset, 0)
	var result ShopPage
	err := s.uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		var err error
		result, err = tx.StoreCards().List(ctx, category, limit, offset)
		return err
	})
	return result, err
}

func categoryPaths(nodes []taxonomy.Category) map[string][]StoreCategory {
	result := map[string][]StoreCategory{}
	var visit func(taxonomy.Category, []StoreCategory)
	visit = func(n taxonomy.Category, parents []StoreCategory) {
		path := append(append([]StoreCategory{}, parents...), StoreCategory{ID: n.ID, Name: n.Name})
		result[n.ID] = path
		for _, child := range n.Children {
			visit(child, path)
		}
	}
	for _, node := range nodes {
		visit(node, nil)
	}
	return result
}

// StorePageSummary возвращает сводку только магазина с доступной страницей.
func (s *Service) StorePageSummary(ctx context.Context, tenant ids.TenantID) (StoreSummary, error) {
	var summary StoreSummary
	err := s.uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		record, err := tx.StoreCards().Get(ctx, tenant)
		if err != nil {
			return err
		}
		if !record.PagePublic {
			return participant.ErrNotFound
		}
		summary = record.Summary
		return nil
	})
	return summary, err
}
