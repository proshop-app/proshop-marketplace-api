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
		result.Participant, err = participantEvent(ctx, tx, tenant, at)
		if err != nil && !errors.Is(err, participant.ErrNotFound) {
			return err
		}
		if len(products) == 0 {
			return nil
		}
		keys := make([]ids.OfferID, 0, len(products))
		for _, product := range products {
			keys = append(keys, ids.OfferID{Tenant: tenant, Product: product})
		}
		states, err := tx.Offers().Batch(ctx, keys)
		if err != nil {
			return err
		}
		for _, state := range states {
			product := state.Product.State()
			p := payload{TenantID: tenant, ProductID: product.ProductID}
			event, err := offerEvent(ctx, tx, p, state, at)
			if err != nil {
				return err
			}
			if event != nil {
				result.Items = append(result.Items, event)
			}
		}
		return nil
	})
	return result, err
}
