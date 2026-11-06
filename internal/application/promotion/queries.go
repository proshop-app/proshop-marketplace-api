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
func (s *Service) Get(ctx context.Context, access Access, id ids.UUID) (Campaign, error) {
	var result Campaign
	err := s.uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		c, err := owned(ctx, tx, access, id)
		if err == nil {
			result, err = campaignView(ctx, tx, c.State())
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
) (Page, int, error) {
	if err := validateDates(filter.From, filter.To); err != nil {
		return Page{}, 0, err
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
		return Page{}, 0, promotion.ErrInvalid
	}
	if filter.Kind != "" && !slices.Contains(
		[]promotion.Kind{promotion.SponsoredSlot, promotion.Banner, promotion.Showcase},
		filter.Kind,
	) {
		return Page{}, 0, promotion.ErrInvalid
	}
	result := Page{Items: []Campaign{}, Counts: map[promotion.Status]int{}}
	var total int
	if access.TenantID != "" {
		filter.TenantID = access.TenantID
	}
	if filter.Limit < 0 || filter.Limit > 200 || filter.Offset < 0 {
		return Page{}, 0, promotion.ErrInvalid
	}
	if filter.Limit == 0 {
		filter.Limit = 50
	}
	err := s.uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		states, count, err := tx.Promotion().List(ctx, filter)
		if err != nil {
			return err
		}
		total = count
		result.Limit, result.Offset = filter.Limit, filter.Offset
		result.Counts, err = tx.Promotion().Counts(ctx, filter)
		if err != nil {
			return err
		}
		for _, status := range []promotion.Status{
			promotion.Draft,
			promotion.Review,
			promotion.Approved,
			promotion.Rejected,
			promotion.Active,
			promotion.Finished,
			promotion.Stopped,
		} {
			if _, ok := result.Counts[status]; !ok {
				result.Counts[status] = 0
			}
		}
		campaignIDs := make([]ids.UUID, len(states))
		for i, state := range states {
			campaignIDs[i] = state.ID
		}
		summaries, err := tx.Promotion().Summaries(ctx, campaignIDs)
		if err != nil {
			return err
		}
		for _, state := range states {
			result.Items = append(result.Items, Campaign{CampaignState: state, Summary: summaries[state.ID]})
		}
		return nil
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
	if from == "" || to == "" {
		return nil, promotion.ErrInvalid
	}
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
			PlacementCode: code,
			Position:      position,
			Target:        promotion.Target{CategoryID: category},
		}
		occupancy, err := tx.Promotion().Occupancy(ctx, state, p.State().Scope, dates, "")
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
