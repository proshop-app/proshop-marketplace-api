package messaging

import (
	"context"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
)

// Parker откладывает сообщения, которые нельзя обработать, в отравленные.
type Parker struct {
	uow ports.UnitOfWork
}

// NewParker — парковка сообщений.
func NewParker(uow ports.UnitOfWork) *Parker {
	return &Parker{uow: uow}
}

// Park сохраняет сообщение; повтор той же позиции ничего не меняет.
func (p *Parker) Park(ctx context.Context, event ports.ParkedEvent) error {
	return p.uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		return tx.Parked().Park(ctx, event)
	})
}
