package ports

import (
	"context"

	"github.com/proshop-app/proshop-marketplace/internal/domain/delivery"
)

// PickupPointsProvider — справочник пунктов выдачи одного перевозчика,
// читаемый учётной записью платформы. Новый перевозчик — новая реализация.
type PickupPointsProvider interface {
	// Carrier — код и название перевозчика.
	Carrier() delivery.CarrierInfo
	// Fetch выгружает весь справочник пачками; ошибка yield прерывает выгрузку.
	Fetch(ctx context.Context, yield func(points []delivery.PickupPoint) error) error
}

// PickupPointQuery — пункты перевозчика в области карты или городе.
type PickupPointQuery struct {
	Carrier delivery.Carrier
	Area    delivery.Area
	Limit   int
}

// PickupPointIndex — геоиндекс пунктов выдачи.
type PickupPointIndex interface {
	// Ensure заводит индекс, если его нет; created — индекс только что заведён.
	Ensure(ctx context.Context) (created bool, err error)
	// Put пишет действующие пункты и убирает закрытые.
	Put(ctx context.Context, points []delivery.PickupPoint) error
	// Search — действующие пункты перевозчика в области.
	Search(ctx context.Context, query PickupPointQuery) ([]delivery.PickupPoint, error)
}
