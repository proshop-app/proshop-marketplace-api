package catalog

import (
	"context"
	"errors"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/catalog"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

// ProductEvent — снимок товара из события catalog.product.projected.
type ProductEvent struct {
	EventID  string
	Snapshot catalog.Snapshot
}

// StockEvent — позиция склада из события stock.item.updated: остаток и прайс.
type StockEvent struct {
	EventID   string
	TenantID  ids.TenantID
	ProductID ids.ProductID
	Stock     catalog.StockSignal
	Price     *catalog.PriceSignal
}

// PriceEvent — новый прайс из события catalog.product.price-updated.
type PriceEvent struct {
	EventID   string
	TenantID  ids.TenantID
	ProductID ids.ProductID
	Price     catalog.PriceSignal
}

// Projector принимает события магазинов в проекцию. Каждое событие — одна
// транзакция: отметка в inbox, изменение проекции и задача синхронизации
// индекса фиксируются вместе.
type Projector struct {
	uow   ports.UnitOfWork
	clock ports.Clock
}

// NewProjector — приём проекций.
func NewProjector(uow ports.UnitOfWork, clock ports.Clock) *Projector {
	return &Projector{uow: uow, clock: clock}
}

// ApplyProduct применяет снимок товара.
//
// Задача синхронизации ставится и для магазина, который ещё не участвует:
// проверка участия здесь гонялась бы с заведением участника в соседней
// транзакции, и новый товар мог бы не попасть в индекс. Индексатор сам
// пропускает товары магазинов без участия, а невзятые задачи по одному
// товару склеиваются.
func (p *Projector) ApplyProduct(ctx context.Context, event ProductEvent) error {
	now := p.clock.Now()
	snapshot := event.Snapshot

	return p.uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		definition, err := tx.Taxonomy().Current(ctx)
		if err != nil {
			return err
		}
		fresh, err := tx.Inbox().MarkProcessed(ctx, event.EventID, now)
		if err != nil || !fresh {
			return err
		}

		product, err := tx.Products().Get(ctx, snapshot.TenantID, snapshot.ProductID)
		changed := true

		switch {
		case errors.Is(err, catalog.ErrProductNotFound):
			product, err = catalog.NewProduct(snapshot, now)
		case err == nil:
			changed, err = product.Apply(snapshot, now)
		}
		if err != nil || !changed {
			return err
		}

		product.Revalidate(definition, now)
		if err := tx.Products().Save(ctx, product); err != nil {
			return err
		}

		return tx.Outbox().Enqueue(ctx, SyncOfferTask(snapshot.TenantID, snapshot.ProductID, now))
	})
}

// ApplyStock применяет остаток и прайс позиции склада.
func (p *Projector) ApplyStock(ctx context.Context, event StockEvent) error {
	return p.applySignals(
		ctx,
		event.EventID,
		event.TenantID,
		event.ProductID,
		func(signals *catalog.Signals) (bool, error) {
			stockChanged, err := signals.ApplyStock(event.Stock, p.clock.Now())
			if err != nil || event.Price == nil {
				return stockChanged, err
			}

			priceChanged, err := signals.ApplyPrice(*event.Price, p.clock.Now())

			return stockChanged || priceChanged, err
		},
	)
}

// ApplyPrice применяет новый прайс товара.
func (p *Projector) ApplyPrice(ctx context.Context, event PriceEvent) error {
	return p.applySignals(
		ctx,
		event.EventID,
		event.TenantID,
		event.ProductID,
		func(signals *catalog.Signals) (bool, error) {
			return signals.ApplyPrice(event.Price, p.clock.Now())
		},
	)
}

func (p *Projector) applySignals(
	ctx context.Context,
	eventID string,
	tenantID ids.TenantID,
	productID ids.ProductID,
	apply func(*catalog.Signals) (bool, error),
) error {
	now := p.clock.Now()

	return p.uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		fresh, err := tx.Inbox().MarkProcessed(ctx, eventID, now)
		if err != nil || !fresh {
			return err
		}

		signals, err := tx.Signals().Get(ctx, tenantID, productID)
		if errors.Is(err, catalog.ErrSignalsNotFound) {
			signals, err = catalog.NewSignals(tenantID, productID), nil
		}
		if err != nil {
			return err
		}

		changed, err := apply(signals)
		if err != nil || !changed {
			return err
		}

		if err := tx.Signals().Save(ctx, signals); err != nil {
			return err
		}

		return tx.Outbox().Enqueue(ctx, SyncOfferTask(tenantID, productID, now))
	})
}
