package participant

import (
	"context"
	"errors"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

const (
	storeDefaultLimit = 20
	storeMaxLimit     = 100
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

func shopHeader(p *participant.Participant, profile participant.Profile, summary StoreSummary) ShopHeader {
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
		available, err := tx.TenantPurge().Available(ctx, []ids.TenantID{task.TenantID})
		if err != nil || !available[task.TenantID] {
			return err
		}
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
		tenant, err := tx.Registry().Get(ctx, task.TenantID)
		if err != nil {
			return err
		}
		cards := tx.StoreCards()
		visible := participant.VisibilityOf(p, tenant).Visible
		summary, more, err := summarizeStore(ctx, tx, p, visible)
		if err != nil {
			return err
		}
		if more {
			if !visible {
				if err := cards.Hide(ctx, task.TenantID); err != nil {
					return err
				}
			}
			return tx.Outbox().Enqueue(ctx, ports.StoreCardTask(task.TenantID, s.clock.Now()))
		}

		profile := p.PublicProfile()
		h := shopHeader(p, profile, summary)
		card := ShopCard{
			Products:       summary.Products,
			Rating:         h.Rating,
			TenantID:       h.TenantID,
			Slug:           h.Slug,
			Name:           h.Name,
			Logo:           h.Logo,
			Mark:           profile.Mark,
			Offers:         h.Offers,
			FromPrice:      summary.FromPrice,
			MoreCategories: max(len(h.Categories)-1, 0),
			IsNew:          h.IsNew,
		}
		if len(h.Categories) > 0 {
			c := h.Categories[0]
			card.Category = &ShopCategory{ID: c.ID, Name: c.Name}
		}
		pagePublic := visible && summary.VisibleOffers > 0
		return cards.Save(ctx, ports.StoreRecord{
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

// ShopDirectory читает изменения публичного справочника после курсора.
func (s *Service) ShopDirectory(ctx context.Context, previous int64) (ports.ShopDirectory, error) {
	var result ports.ShopDirectory
	err := s.uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		var err error
		result, err = tx.StoreCards().Directory(ctx, previous)
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
