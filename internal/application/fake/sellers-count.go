package fake

import (
	"context"
	"sync"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
)

// SellersCounts использует доменную видимость, чтобы тесты не дублировали SQL.
type SellersCounts struct {
	mu     sync.Mutex
	States []ports.OfferState
	Counts map[string]int
	Err    error
	Calls  int
	Keys   []string
}

// CountSellers учитывает варианты одного магазина только один раз.
func (s *SellersCounts) CountSellers(ctx context.Context, keys []string) (map[string]int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Calls++
	s.Keys = append([]string(nil), keys...)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s.Err != nil {
		return nil, s.Err
	}
	if s.Counts != nil {
		return s.Counts, nil
	}
	sellers := make(map[string]map[string]bool, len(keys))
	for _, key := range keys {
		sellers[key] = make(map[string]bool)
	}
	for _, state := range s.States {
		visibility := participant.VisibilityOf(state.Participant, state.Tenant)
		offer := catalog.ComposeOffer(
			state.Product,
			state.Signals,
			catalog.Seller{Visible: visibility.Visible},
			state.Verdict,
		)
		if group := sellers[offer.Card.ModelKey]; offer.Visible && group != nil {
			group[string(offer.ID.Tenant)] = true
		}
	}
	counts := make(map[string]int, len(keys))
	for key, group := range sellers {
		counts[key] = len(group)
	}
	return counts, nil
}
