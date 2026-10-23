package participant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
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

// CheckItems — цена и наличие позиций у магазина.
func (p *ResyncingPlatform) CheckItems(
	ctx context.Context,
	tenantID ids.TenantID,
	items []ports.ItemRequest,
) ([]ports.ItemCheck, error) {
	checks, err := p.PlatformAPI.CheckItems(ctx, tenantID, items)

	return checks, p.resync(ctx, tenantID, err)
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

// resync ставит сверку настроек, если сервер ответил, что магазин не
// участвует. Задача ставится и при отменённом запросе покупателя;
// повторный отказ до её разбора склеивается с ней по ключу.
func (p *ResyncingPlatform) resync(ctx context.Context, tenantID ids.TenantID, err error) error {
	if !errors.Is(err, ports.ErrMarketplaceDisabled) {
		return err
	}

	task := SyncSettingsTask(tenantID, p.clock.Now())
	enqueue := func(ctx context.Context, tx ports.Tx) error {
		return tx.Outbox().Enqueue(ctx, task)
	}

	if failed := p.uow.Do(context.WithoutCancel(ctx), enqueue); failed != nil {
		return errors.Join(err, fmt.Errorf("сверка настроек %s не поставлена: %w", tenantID, failed))
	}

	return err
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
