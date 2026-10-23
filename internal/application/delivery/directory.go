// Package delivery — справочники пунктов выдачи перевозчиков: обновление
// своими учётными записями, геоиндекс и поиск для карты.
package delivery

import (
	"context"
	"errors"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/delivery"
)

const (
	// DefaultLimit — пунктов в ответе по умолчанию.
	DefaultLimit = 200
	// MaxLimit — наибольшее число пунктов в ответе.
	MaxLimit = 1000
	// indexBatch — пунктов за один проход переноса в индекс.
	indexBatch = 1000
	// leaseTTL — аренда обновления одного перевозчика.
	leaseTTL = time.Hour
)

// RefreshResult — итог обновления справочника перевозчика.
type RefreshResult struct {
	Carrier     delivery.Carrier
	Fetched     int
	Deactivated int64
	Skipped     bool
	Err         error
}

// Directory — справочники пунктов выдачи всех подключённых перевозчиков.
type Directory struct {
	uow       ports.UnitOfWork
	providers []ports.PickupPointsProvider
	index     ports.PickupPointIndex
	leases    ports.Leases
	clock     ports.Clock
	holder    string
}

// NewDirectory — справочники; holder — имя процесса для аренды обновления.
func NewDirectory(uow ports.UnitOfWork, providers []ports.PickupPointsProvider, index ports.PickupPointIndex,
	leases ports.Leases, clock ports.Clock, holder string) *Directory {
	return &Directory{uow: uow, providers: providers, index: index, leases: leases, clock: clock, holder: holder}
}

// Carriers — подключённые перевозчики в порядке подключения.
func (d *Directory) Carriers() []delivery.CarrierInfo {
	carriers := make([]delivery.CarrierInfo, 0, len(d.providers))
	for _, provider := range d.providers {
		carriers = append(carriers, provider.Carrier())
	}

	return carriers
}

// Search — пункты одного перевозчика в области карты или городе.
func (d *Directory) Search(
	ctx context.Context,
	carrier delivery.Carrier,
	area delivery.Area,
	limit int,
) ([]delivery.PickupPoint, error) {
	if !d.known(carrier) {
		return nil, delivery.ErrUnknownCarrier
	}
	if err := area.Validate(); err != nil {
		return nil, err
	}

	switch {
	case limit == 0:
		limit = DefaultLimit
	case limit < 0 || limit > MaxLimit:
		return nil, delivery.ErrInvalidArea.WithMessage("limit — от 1 до 1000")
	}

	return d.index.Search(ctx, ports.PickupPointQuery{Carrier: carrier, Area: area, Limit: limit})
}

// Point — действующий пункт из справочника.
func (d *Directory) Point(ctx context.Context, carrier delivery.Carrier, code string) (delivery.PickupPoint, error) {
	if !d.known(carrier) {
		return delivery.PickupPoint{}, delivery.ErrUnknownCarrier
	}

	var point delivery.PickupPoint

	err := d.uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		var err error
		point, err = tx.PickupPoints().Get(ctx, carrier, code)

		return err
	})
	if err != nil {
		return delivery.PickupPoint{}, err
	}
	if !point.Active {
		return delivery.PickupPoint{}, delivery.ErrPickupPointNotFound
	}

	return point, nil
}

// Refresh обновляет справочники всех перевозчиков. Перевозчик, чью выгрузку
// уже ведёт другой процесс, пропускается. Пункты, не пришедшие в полной
// выгрузке, закрываются; при обрыве выгрузки ничего не закрывается.
func (d *Directory) Refresh(ctx context.Context) []RefreshResult {
	results := make([]RefreshResult, 0, len(d.providers))

	for _, provider := range d.providers {
		results = append(results, d.refresh(ctx, provider))
	}

	return results
}

func (d *Directory) refresh(ctx context.Context, provider ports.PickupPointsProvider) RefreshResult {
	carrier := provider.Carrier().Code
	result := RefreshResult{Carrier: carrier}
	lease := "pickup-points:" + string(carrier)

	acquired, err := d.leases.Acquire(ctx, lease, d.holder, leaseTTL)
	if err != nil || !acquired {
		result.Skipped, result.Err = !acquired && err == nil, err

		return result
	}

	defer func() { _ = d.leases.Release(context.WithoutCancel(ctx), lease, d.holder) }()

	started := d.clock.Now()

	result.Err = provider.Fetch(ctx, func(points []delivery.PickupPoint) error {
		for i := range points {
			if points[i].Carrier != carrier {
				message := "пункт чужого перевозчика в выгрузке " + string(carrier)

				return delivery.ErrInvalidPickupPoint.WithMessage(message)
			}

			points[i].RefreshedAt = started
		}

		if err := d.uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
			return tx.PickupPoints().Upsert(ctx, points)
		}); err != nil {
			return err
		}

		result.Fetched += len(points)

		return d.index.Put(ctx, points)
	})
	if result.Err != nil {
		return result
	}

	result.Err = d.uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		var err error
		result.Deactivated, err = tx.PickupPoints().DeactivateStale(ctx, carrier, started)

		return err
	})
	if result.Err == nil && result.Deactivated > 0 {
		result.Err = d.Reindex(ctx, carrier)
	}

	return result
}

// Reindex переносит справочник перевозчика из базы в индекс: действующие
// пункты пишутся, закрытые убираются.
func (d *Directory) Reindex(ctx context.Context, carrier delivery.Carrier) error {
	after := ""

	for {
		var page []delivery.PickupPoint

		err := d.uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
			var err error
			page, err = tx.PickupPoints().Page(ctx, carrier, after, indexBatch)

			return err
		})
		if err != nil {
			return err
		}
		if len(page) == 0 {
			return nil
		}

		if err := d.index.Put(ctx, page); err != nil {
			return err
		}

		after = page[len(page)-1].Code
		if len(page) < indexBatch {
			return nil
		}
	}
}

// EnsureIndex заводит геоиндекс; только что заведённый наполняется из базы.
func (d *Directory) EnsureIndex(ctx context.Context) error {
	created, err := d.index.Ensure(ctx)
	if err != nil || !created {
		return err
	}

	var errs []error
	for _, provider := range d.providers {
		errs = append(errs, d.Reindex(ctx, provider.Carrier().Code))
	}

	return errors.Join(errs...)
}

func (d *Directory) known(carrier delivery.Carrier) bool {
	for _, provider := range d.providers {
		if provider.Carrier().Code == carrier {
			return true
		}
	}

	return false
}
