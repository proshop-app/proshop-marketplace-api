package participant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/checkout"
	"github.com/proshop-app/proshop-marketplace/internal/domain/delivery"
	"github.com/proshop-app/proshop-marketplace/pkg/errs"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

// TaskSyncSettings — внеочередная сверка настроек одного магазина.
const TaskSyncSettings = "participant.settings.sync"

// ErrInvalidTask — задача сверки настроек повреждена.
var ErrInvalidTask = errs.New(errs.Invalid, "participant.invalid_task", "некорректная задача сверки настроек")

type settingsTaskPayload struct {
	TenantID string `json:"tenantId"`
}

// ResyncingPlatform — API сервера для корзины и покупки. Отказ «магазин не
// участвует» (ports.ErrMarketplaceDisabled) ставит внеочередную сверку
// настроек этого магазина: его предложения уходят из выдачи, не дожидаясь
// планового прохода. Сама ошибка возвращается как есть.
type ResyncingPlatform struct {
	ports.PlatformAPI
	uow   ports.UnitOfWork
	clock ports.Clock
}

// NewResyncingPlatform — API сервера со сверкой настроек по отказу.
func NewResyncingPlatform(platform ports.PlatformAPI, uow ports.UnitOfWork, clock ports.Clock) *ResyncingPlatform {
	return &ResyncingPlatform{PlatformAPI: platform, uow: uow, clock: clock}
}

// QuoteDelivery — стоимость и срок доставки по договору магазина.
func (p *ResyncingPlatform) QuoteDelivery(
	ctx context.Context,
	tenantID ids.TenantID,
	request ports.QuoteRequest,
) (delivery.Offered, error) {
	offered, err := p.PlatformAPI.QuoteDelivery(ctx, tenantID, request)

	return offered, p.resync(ctx, tenantID, err)
}

// PickupPoints обновляет устаревшую проекцию подключения службы по определённому отказу источника.
func (p *ResyncingPlatform) PickupPoints(
	ctx context.Context,
	tenantID ids.TenantID,
	carrier delivery.Carrier,
	city string,
) (ports.PickupPoints, error) {
	answer, err := p.PlatformAPI.PickupPoints(ctx, tenantID, carrier, city)
	if err == nil && !answer.Available && answer.Reason == delivery.ReasonCarrierNotConnected {
		return answer, p.syncSettings(ctx, tenantID)
	}
	return answer, p.resync(ctx, tenantID, err)
}

// resync ставит сверку настроек, если сервер ответил, что магазин не
// участвует. Повторный отказ до её разбора склеивается с ней по ключу.
// Отменённый запрос не должен удерживать соединение для необязательной сверки.
func (p *ResyncingPlatform) resync(ctx context.Context, tenantID ids.TenantID, err error) error {
	if !errors.Is(err, ports.ErrMarketplaceDisabled) {
		return err
	}
	if failed := p.syncSettings(ctx, tenantID); failed != nil {
		return errors.Join(err, fmt.Errorf("сверка настроек %s не поставлена: %w", tenantID, failed))
	}

	return err
}

func (p *ResyncingPlatform) syncSettings(ctx context.Context, tenantID ids.TenantID) error {
	task := SyncSettingsTask(tenantID, p.clock.Now())
	return p.uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		return tx.Outbox().Enqueue(ctx, task)
	})
}

// SyncSettingsTask — задача внеочередной сверки настроек магазина.
func SyncSettingsTask(tenantID ids.TenantID, at time.Time) ports.NewTask {
	payload, _ := json.Marshal(settingsTaskPayload{TenantID: string(tenantID)})

	return ports.NewTask{
		Kind:     TaskSyncSettings,
		TenantID: tenantID,
		DedupKey: TaskSyncSettings + ":" + string(tenantID),
		Payload:  payload,
		At:       at,
	}
}

// CreateOrder сохраняет отказ участия для внеочередной сверки настроек.
func (p *ResyncingPlatform) CreateOrder(
	ctx context.Context,
	tenantID ids.TenantID,
	request ports.CreateOrderRequest,
) (checkout.SellerOrder, error) {
	order, err := p.PlatformAPI.CreateOrder(ctx, tenantID, request)
	return order, p.resync(ctx, tenantID, err)
}

// PriceGroup сверяет настройки и при отказе расчёта скидок группы.
func (p *ResyncingPlatform) PriceGroup(
	ctx context.Context,
	tenant ids.TenantID,
	request ports.GroupPricingRequest,
) (ports.GroupPricing, error) {
	result, err := p.PlatformAPI.PriceGroup(ctx, tenant, request)
	return result, p.resync(ctx, tenant, err)
}

// CancelOrder сохраняет отказ участия для внеочередной сверки настроек.
func (p *ResyncingPlatform) CancelOrder(
	ctx context.Context,
	tenant ids.TenantID,
	request ports.CancelOrderRequest,
) (ports.CancelOrderResult, error) {
	result, err := p.PlatformAPI.CancelOrder(ctx, tenant, request)
	return result, p.resync(ctx, tenant, err)
}
