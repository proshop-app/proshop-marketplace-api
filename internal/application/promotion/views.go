package promotion

import (
	"context"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/promotion"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

// Campaign содержит снимок и итоги из одной транзакции сценария.
type Campaign struct {
	promotion.CampaignState
	Summary promotion.Summary
}

// Page содержит счётчики полного фильтра и страницу.
type Page struct {
	Items  []Campaign
	Counts map[promotion.Status]int
	Limit  int
	Offset int
}

func campaignView(ctx context.Context, tx ports.Tx, state promotion.CampaignState) (Campaign, error) {
	summaries, err := tx.Promotion().Summaries(ctx, []ids.UUID{state.ID})
	return Campaign{CampaignState: state, Summary: summaries[state.ID]}, err
}

// Delete удаляет черновик и журнал в одной транзакции с проверкой владельца.
func (s *Service) Delete(ctx context.Context, access Access, id ids.UUID) error {
	return s.uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		c, err := owned(ctx, tx, access, id)
		if err != nil {
			return err
		}
		if c.State().Status != promotion.Draft {
			return promotion.ErrConflict
		}
		return tx.Promotion().Delete(ctx, c)
	})
}

// NextMonday возвращает ближайший понедельник строго после текущей даты.
func (s *Service) NextMonday() string {
	day := s.clock.Now().In(s.config.Location).AddDate(0, 0, 1)
	for day.Weekday() != time.Monday {
		day = day.AddDate(0, 0, 1)
	}
	return day.Format(time.DateOnly)
}

// AllowEvents использует общий порт оконного лимитера по адресу клиента.
func (s *Service) AllowEvents(ctx context.Context, address string, count int) error {
	if count < 0 || count > 50 {
		return promotion.ErrInvalid
	}
	if s.config.Limiter == nil {
		return nil
	}
	for range count {
		allowed, _, err := s.config.Limiter.Allow(
			ctx,
			"promotion:events:"+address,
			s.config.EventsPerMinute,
			time.Minute,
		)
		if err != nil {
			return err
		}
		if !allowed {
			return promotion.ErrRateLimited
		}
	}
	return nil
}
