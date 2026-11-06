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

// PromotionRandom выбирает заданный элемент для воспроизводимых проверок ротации.
type PromotionRandom struct{}

// Search не выполняет собственное ранжирование: тест задаёт результат поискового сервиса.
func (i *PromotionIndex) Search(_ context.Context, q ports.OfferQuery) (ports.OfferPage, error) {
	i.Query = q
	return i.Result, nil
}

// IntN выбирает последний элемент; равномерность обеспечивает производственный генератор.
func (PromotionRandom) IntN(n int) int { return n - 1 }
