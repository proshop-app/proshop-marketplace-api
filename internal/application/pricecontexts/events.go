package pricecontexts

import (
	"context"
	"errors"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/buyer"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

// Changed — событие сбрасывает весь снимок покупателя, поэтому порядок ревизий не может вернуть старую цену.
type Changed struct {
	EventID  string
	BuyerID  ids.UUID
	TenantID ids.TenantID
}

// Invalidate отмечает inbox только после успешного сброса; повторный DELETE безопасен при откате транзакции.
func (s *Service) Invalidate(ctx context.Context, event Changed) error {
	outcome := "duplicate"
	err := s.uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		fresh, err := tx.Inbox().MarkProcessed(ctx, event.EventID, s.clock.Now())
		if err != nil || !fresh {
			return err
		}
		_, err = tx.Buyers().Get(ctx, event.BuyerID)
		if err != nil && !errors.Is(err, buyer.ErrNotFound) {
			return err
		}
		outcome = "invalidated"
		if errors.Is(err, buyer.ErrNotFound) {
			outcome = "unknown_buyer"
		}
		// Удаление также отменяет загрузку, начатую до события, в любой реплике с общим Redis.
		return s.cache.Delete(ctx, event.BuyerID)
	})
	if err != nil {
		outcome = "error"
	}
	if s.metrics != nil {
		s.metrics.PriceContextInvalidation(event.TenantID, outcome)
	}
	return err
}
