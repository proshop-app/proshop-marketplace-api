package catalog

import (
	"context"
	"errors"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/moderationstatus"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/catalog"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

// ProductEvent — снимок товара из основной или массовой темы проекции. Повтор и старый снимок
// отсекает ревизия источника, идентификатор события для дедупа не нужен.
type ProductEvent struct {
	Snapshot catalog.Snapshot
}

// StockEvent — позиция склада из события stock.item.updated: остаток и прайс. Повтор и старый
// сигнал отсекает время публикации, идентификатор события для дедупа не нужен. Два события одной
// позиции с одним временем и разными значениями при повторной доставке хвоста дают лишнюю
// запись и задачу синхронизации, но итог сходится к последнему событию: ключ порядка держит их
// последовательность.
type StockEvent struct {
	TenantID  ids.TenantID
	ProductID ids.ProductID
	Stock     catalog.StockSignal
	Price     *catalog.PriceSignal
}

// Projector принимает события магазинов в проекцию. Снимки товаров идут пачкой в одной
// транзакции; изменение проекции и задачи синхронизации индекса фиксируются вместе.
type Projector struct {
	evaluator ports.ProjectionModerator
	uow       ports.UnitOfWork
	clock     ports.Clock
}

// NewProjector — приём проекций.
func NewProjector(uow ports.UnitOfWork, clock ports.Clock, evaluator ports.ProjectionModerator) *Projector {
	return &Projector{
		evaluator: evaluator,
		uow:       uow,
		clock:     clock,
	}
}

// ApplyProduct применяет один снимок товара — пачку из одного.
func (p *Projector) ApplyProduct(ctx context.Context, event ProductEvent) error {
	return p.ApplyProducts(ctx, []ProductEvent{event})
}

// ApplyProducts применяет снимки разных товаров одной транзакцией. Повтор и старый снимок
// отсекает ревизия источника (Product.Apply не меняет запись с ревизией не новее), поэтому
// отметка processed_events не нужна. Сбой любого снимка откатывает пачку целиком: приём
// повторяет её сообщения по одному.
//
// Задача синхронизации ставится и для магазина, который ещё не участвует:
// проверка участия здесь гонялась бы с заведением участника в соседней
// транзакции, и новый товар мог бы не попасть в индекс. Индексатор сам
// пропускает товары магазинов без участия, а невзятые задачи по одному
// товару склеиваются.
func (p *Projector) ApplyProducts(ctx context.Context, events []ProductEvent) error {
	if len(events) == 0 {
		return nil
	}
	now := p.clock.Now()
	return p.uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		batch := newProjectionBatch(tx, events)
		for _, event := range events {
			if err := p.applyProduct(ctx, batch, event.Snapshot, now); err != nil {
				return err
			}
		}
		return batch.flush(ctx)
	})
}

func (p *Projector) applyProduct(ctx context.Context, tx ports.Tx, snapshot catalog.Snapshot, now time.Time) error {
	product, previous, err := projectedProduct(ctx, tx, snapshot, now)
	if err != nil || product == nil {
		return err
	}
	reused, err := p.validateProjection(ctx, tx, product, previous, now)
	if err != nil {
		return err
	}
	state := product.State()
	tasks := make([]ports.NewTask, 0, 2)
	if !reused {
		tasks = append(tasks, moderationstatus.OfferTask(state.TenantID, state.ProductID, now))
	}
	tasks = append(tasks, SyncOfferTask(state.TenantID, state.ProductID, now))
	return tx.Outbox().EnqueueBatch(ctx, tasks)
}

// ApplyStock применяет остаток и прайс позиции склада.
func (p *Projector) ApplyStock(ctx context.Context, event StockEvent) error {
	return p.applySignals(
		ctx,
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

func (p *Projector) applySignals(
	ctx context.Context,
	tenantID ids.TenantID,
	productID ids.ProductID,
	apply func(*catalog.Signals) (bool, error),
) error {
	now := p.clock.Now()

	return p.uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
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
