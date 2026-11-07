package fake

import (
	"context"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

// TaxonomyEvaluator наблюдает вызовы и позволяет оборвать проход на конкретном товаре.
type TaxonomyEvaluator struct {
	Calls     []ids.OfferID
	FailOffer ids.OfferID
	Err       error
	FailOnce  bool
}

// Evaluate записывает вызов и возвращает заданный сбой для выбранного товара.
func (e *TaxonomyEvaluator) Evaluate(
	_ context.Context,
	_ ports.Tx,
	product *catalog.Product,
	_ *participant.Participant,
	_ time.Time,
) error {
	state := product.State()
	key := ids.OfferID{Tenant: state.TenantID, Product: state.ProductID}
	e.Calls = append(e.Calls, key)
	if key == e.FailOffer {
		err := e.Err
		if e.FailOnce {
			e.Err = nil
		}
		return err
	}
	return nil
}
