package participant

import (
	"context"
	"errors"
	"sort"
	"strings"
	"unicode"

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
)

// StoreCategory — счётчик корневой категории магазина.
type StoreCategory = ports.StoreCategory

// StoreSummary — внутренняя статистика магазина.
type StoreSummary = ports.StoreSummary

// ShopColor — цвет публичной карточки с контрастом, рассчитанным сервером.
type ShopColor = ports.ShopColor

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

func summarizeStore(ctx context.Context, tx ports.Tx, p *participant.Participant) (StoreSummary, error) {
	result := StoreSummary{
		Categories: []StoreCategory{},
		Hidden:     append([]participant.CardHidden{}, p.State().StoreCard.Hidden...),
	}
	definition, err := tx.Taxonomy().Current(ctx)
	if err != nil {
		return result, err
	}
	roots := categoryRoots(definition.Categories)
	paths := categoryPaths(definition.Categories)
	result.CategoryCounts = map[string]StoreCategory{}
	var visible, candidates []storeOffer
	for after := ids.ProductID(""); ; {
		states, err := tx.Offers().StoreCardOffers(ctx, p.TenantID(), after, storeBatchSize)
		if err != nil {
			return result, err
		}
		for _, state := range states {
			product := state.Product.State()
			after = product.ProductID
			if product.Deleted {
				continue
			}
			card := product.Card
			if card.Marketplace != nil && card.Marketplace.Published {
				if _, ok := roots[card.Marketplace.CategoryID]; !ok {
					result.Uncategorized++
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
	return result, nil
}

func shopHeader(p *participant.Participant, summary StoreSummary) ShopHeader {
	profile := p.PublicProfile()
	initial := ""
	for _, letter := range p.Name() {
		if unicode.IsLetter(letter) || unicode.IsNumber(letter) {
			initial = strings.ToUpper(string(letter))
			break
		}
	}
	color := participant.NeutralCardColor
	if profile.Color != nil && profile.Color.Card != "" {
		color = profile.Color.Card
	}
	return ShopHeader{
		Rating:     summary.Rating,
		TenantID:   p.TenantID(),
		Slug:       p.Slug(),
		Name:       p.Name(),
		Logo:       profile.Logo,
		Initial:    initial,
		Color:      ShopColor{Card: color},
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
			Rating:         h.Rating,
			TenantID:       h.TenantID,
			Slug:           h.Slug,
			Name:           h.Name,
			Logo:           h.Logo,
			Initial:        h.Initial,
			Color:          h.Color,
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
