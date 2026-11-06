package promotion

import (
	"context"
	"errors"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
	"github.com/proshop-app/proshop-marketplace/internal/domain/promotion"
)

// Period задаёт включительные границы календарных недель.
func (s *Service) Period(from string, weeks int) (promotion.Period, error) {
	return promotion.NewPeriod(from, weeks, s.config.Location)
}

// Weeks проверяет календарные границы и перечисляет понедельники.
func (s *Service) Weeks(period promotion.Period) (int, error) {
	weeks, err := period.Weeks(s.config.Location)
	return len(weeks), err
}

// Summary обогащает ответ названием магазина и накопленной статистикой.
func (s *Service) Summary(
	ctx context.Context,
	state promotion.CampaignState,
) (string, []promotion.Counter, error) {
	var name string
	var rows []promotion.Counter
	err := s.uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		p, err := tx.Participants().Get(ctx, state.TenantID)
		if err != nil && !errors.Is(err, participant.ErrNotFound) {
			return err
		}
		if p != nil {
			name = p.Name()
		}
		rows, err = tx.Promotion().Stats(ctx, state.ID, "", "")
		return err
	})
	return name, rows, err
}
