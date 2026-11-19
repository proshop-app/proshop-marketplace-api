package participant

import (
	"context"
	"errors"
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
				Categories: []StoreCategory{},
				Products:   []ports.ShopCardProduct{},
				Hidden:     append([]participant.CardHidden{}, p.State().StoreCard.Hidden...),
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

func storeProduct(item storeOffer) ports.ShopCardProduct {
	var image *string
	if item.product.Card.Image != "" {
		value := item.product.Card.Image
		image = &value
	}
	return ports.ShopCardProduct{
		ProductID: string(item.product.ProductID),
		OfferID:   item.offer.ID.String(),
		Name:      item.product.Card.Name,
		Image:     image,
	}
}

func shopHeader(p *participant.Participant, summary StoreSummary) ShopHeader {
	profile := p.PublicProfile()
	return ShopHeader{
		Rating:     summary.Rating,
		TenantID:   p.TenantID(),
		Slug:       p.Slug(),
		Name:       p.Name(),
		Logo:       profile.Logo,
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
			Products:       summary.Products,
			Rating:         h.Rating,
			TenantID:       h.TenantID,
			Slug:           h.Slug,
			Name:           h.Name,
			Logo:           h.Logo,
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
