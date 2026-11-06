package promotion

import (
	"context"
	"slices"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/promotion"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

// Availability возвращает цену и свободные слоты по неделям.
type Availability struct {
	WeeklyPriceKopecks int64           `json:"weeklyPriceKopecks"`
	TotalKopecks       int64           `json:"totalKopecks"`
	Weeks              []AvailableWeek `json:"weeks"`
}

// AvailableWeek показывает свободный остаток без отрицательных значений.
type AvailableWeek struct {
	From string `json:"from"`
	Free string `json:"free"`
}

// Get читает размещение с проверкой области доступа.
func (s *Service) Get(ctx context.Context, access Access, id ids.UUID) (promotion.CampaignState, error) {
	var result promotion.CampaignState
	err := s.uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		c, err := owned(ctx, tx, access, id)
		if err == nil {
			result = c.State()
		}
		return err
	})
	return result, err
}

// List возвращает страницу размещений и полное число совпадений.
func (s *Service) List(
	ctx context.Context,
	access Access,
	filter promotion.Filter,
) ([]promotion.CampaignState, int, error) {
	if err := validateDates(filter.From, filter.To); err != nil {
		return nil, 0, err
	}
	if filter.Status != "" && !slices.Contains(
		[]promotion.Status{
			promotion.Draft,
			promotion.Review,
			promotion.Approved,
			promotion.Rejected,
			promotion.Active,
			promotion.Finished,
			promotion.Stopped,
		},
		filter.Status,
	) {
		return nil, 0, promotion.ErrInvalid
	}
	if filter.Kind != "" && !slices.Contains(
		[]promotion.Kind{promotion.SponsoredSlot, promotion.Banner, promotion.Showcase},
		filter.Kind,
	) {
		return nil, 0, promotion.ErrInvalid
	}
	var result []promotion.CampaignState
	var total int
	if access.TenantID != "" {
		filter.TenantID = access.TenantID
	}
	if filter.Limit < 0 || filter.Limit > 200 || filter.Offset < 0 {
		return nil, 0, promotion.ErrInvalid
	}
	if filter.Limit == 0 {
		filter.Limit = 50
	}
	err := s.uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		var err error
		result, total, err = tx.Promotion().List(ctx, filter)
		return err
	})
	return result, total, err
}

// Placements возвращает прайс с фильтром доступных мест.
func (s *Service) Placements(ctx context.Context, activeOnly bool) ([]promotion.PlacementState, error) {
	result := []promotion.PlacementState{}
	err := s.uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		all, err := tx.Promotion().Placements(ctx)
		if err != nil {
			return err
		}
		for _, p := range all {
			if !activeOnly || p.Active {
				result = append(result, p)
			}
		}
		return nil
	})
	return result, err
}

// Decisions возвращает журнал в порядке решений.
func (s *Service) Decisions(ctx context.Context, access Access, id ids.UUID) ([]promotion.Decision, error) {
	var result []promotion.Decision
	err := s.uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		if _, err := owned(ctx, tx, access, id); err != nil {
			return err
		}
		var err error
		result, err = tx.Promotion().Decisions(ctx, id)
		return err
	})
	return result, err
}

// Stats читает суточные счётчики за указанный период.
func (s *Service) Stats(ctx context.Context, access Access, id ids.UUID, from, to string) ([]promotion.Counter, error) {
	if err := validateDates(from, to); err != nil {
		return nil, err
	}
	var result []promotion.Counter
	err := s.uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		if _, err := owned(ctx, tx, access, id); err != nil {
			return err
		}
		var err error
		result, err = tx.Promotion().Stats(ctx, id, from, to)
		return err
	})
	return result, err
}

// Availability возвращает цену и свободные слоты по неделям.
func (s *Service) Availability(
	ctx context.Context,
	code string,
	position int,
	from string,
	weeks int,
	category string,
) (Availability, error) {
	result := Availability{Weeks: []AvailableWeek{}}
	period, err := promotion.NewPeriod(from, weeks, s.config.Location)
	if err != nil {
		return result, err
	}
	if err := period.ValidateStart(s.clock.Now(), s.config.Location); err != nil {
		return result, err
	}
	err = s.uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		p, err := tx.Promotion().Placement(ctx, code)
		if err != nil {
			return err
		}
		if p.State().Scope == promotion.Category && category == "" {
			return promotion.ErrOfferWithoutCategory
		}
		total, err := p.Quote(position, weeks)
		if err != nil {
			return err
		}
		dates, err := period.Weeks(s.config.Location)
		if err != nil {
			return err
		}
		state := promotion.CampaignState{
			ID:            ids.UUID("00000000-0000-0000-0000-000000000000"),
			PlacementCode: code,
			Position:      position,
			Target:        promotion.Target{CategoryID: category},
		}
		occupancy, err := tx.Promotion().Occupancy(ctx, state, p.State().Scope, dates)
		if err != nil {
			return err
		}
		result.WeeklyPriceKopecks, result.TotalKopecks = p.Price(position).Kopecks(), total.Kopecks()
		for _, w := range occupancy {
			free := "many"
			switch remaining := p.State().SlotLimit - w.Sold; {
			case remaining <= 0:
				free = "none"
			case remaining == 1:
				free = "one"
			}
			result.Weeks = append(result.Weeks, AvailableWeek{From: w.From, Free: free})
		}
		return nil
	})
	return result, err
}

func validateDates(from, to string) error {
	for _, value := range []string{from, to} {
		if value != "" {
			if _, err := time.Parse(time.DateOnly, value); err != nil {
				return promotion.ErrInvalid
			}
		}
	}
	if from != "" && to != "" && from > to {
		return promotion.ErrInvalid
	}
	return nil
}
