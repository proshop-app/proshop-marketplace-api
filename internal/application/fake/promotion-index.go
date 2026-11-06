package fake

import (
	"context"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
)

// PromotionIndex возвращает заданную выдачу и запоминает фильтры проверки релевантности.
type PromotionIndex struct {
	*Index
	Result ports.OfferPage
	Query  ports.OfferQuery
}

// Search не выполняет собственное ранжирование: тест задаёт результат поискового сервиса.
func (i *PromotionIndex) Search(_ context.Context, q ports.OfferQuery) (ports.OfferPage, error) {
	i.Query = q
	return i.Result, nil
}
