// Package ports — границы приложения с внешним миром. Реализации живут в
// инфраструктуре, в тестах приложения — fake-реализации.
package ports

import (
	"context"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/domain/buyer"
	"github.com/proshop-app/proshop-marketplace/internal/domain/cart"
	"github.com/proshop-app/proshop-marketplace/internal/domain/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/domain/checkout"
	"github.com/proshop-app/proshop-marketplace/internal/domain/delivery"
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
)

// Clock — текущее время.
type Clock interface {
	Now() time.Time
}

// UnitOfWork — транзакция: всё, что сделано внутри fn, фиксируется вместе или
// не фиксируется вовсе. Ошибка fn откатывает транзакцию и возвращается как есть.
type UnitOfWork interface {
	Do(ctx context.Context, fn func(ctx context.Context, tx Tx) error) error
}

// Tx — хранилища, привязанные к одной транзакции.
type Tx interface {
	Participants() participant.Repository
	Registry() participant.Registry
	Products() catalog.ProductRepository
	Signals() catalog.SignalsRepository
	Tags() catalog.TagRepository
	Offers() OfferStates
	Outbox() Outbox
	Inbox() Inbox
	Parked() ParkedEvents
	Buyers() buyer.Repository
	Challenges() buyer.ChallengeRepository
	Carts() cart.Repository
	Checkouts() checkout.Repository
	PickupPoints() delivery.PickupPointRepository
}
