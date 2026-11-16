package moderationstatus

import (
	"context"
	"errors"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/moderation"
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

// MaxProductIDs ограничивает одну пачку статусов магазина.
const MaxProductIDs = 200

// Snapshot содержит текущую обратную связь; неизвестные и удалённые товары пропускаются.
type Snapshot struct {
	Items       []*OfferEvent     `json:"items"`
	Participant *ParticipantEvent `json:"participant"`
}

// Get использует тот же расчёт, что и outbox, без публикации событий и записи состояния.
func (s *Service) Get(ctx context.Context, tenant ids.TenantID, products []ids.ProductID) (Snapshot, error) {
	result := Snapshot{Items: []*OfferEvent{}}
	if len(products) > MaxProductIDs {
		return result, moderation.ErrInvalid
	}
	err := s.uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		at := s.clock.Now().UTC().Format(time.RFC3339)
		var err error
		if len(products) > 0 {
			result.Items, err = offerEvents(ctx, tx, tenant, products, at)
			return err
		}
		result.Participant, err = participantEvent(ctx, tx, tenant, at)
		if errors.Is(err, participant.ErrNotFound) {
			return nil
		}
		return err
	})
	return result, err
}
