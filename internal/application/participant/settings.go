package participant

import (
	"context"
	"encoding/json"
	"errors"

	appcatalog "github.com/proshop-app/proshop-marketplace/internal/application/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

// settingsPage — участников за один проход сверки.
const settingsPage = 200

// SettingsResult — итог сверки настроек магазинов.
type SettingsResult struct {
	Candidates int
	Queue      int
	Checked    int
	Changed    int
	Failed     map[ids.TenantID]error
}

// Settings сверяет с сервером обязательные настройки доставки.
// Изменение настроек меняет видимость предложений — магазин
// ставится на переиндексацию в той же транзакции.
type Settings struct {
	ids      ports.IDs
	uow      ports.UnitOfWork
	platform ports.PlatformAPI
	clock    ports.Clock
}

// NewSettings — сверка настроек магазинов.
func NewSettings(uow ports.UnitOfWork, platform ports.PlatformAPI, clock ports.Clock, identities ports.IDs) *Settings {
	return &Settings{uow: uow, platform: platform, clock: clock, ids: identities}
}

// Sync сверяет всех участников. Сбой по одному магазину не останавливает остальных.
func (s *Settings) Sync(ctx context.Context) (SettingsResult, error) {
	result := SettingsResult{Failed: map[ids.TenantID]error{}}
	if err := s.backfill(ctx, result.Failed); err != nil {
		return result, err
	}

	for offset := 0; ; offset += settingsPage {
		var tenants []ids.TenantID

		err := s.uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
			items, _, err := tx.Participants().List(ctx, participant.Page{Limit: settingsPage, Offset: offset})
			for _, p := range items {
				tenants = append(tenants, p.TenantID())
			}

			return err
		})
		if err != nil {
			return result, err
		}

		for _, tenant := range tenants {
			changed, err := s.SyncOne(ctx, tenant)
			result.Checked++

			switch {
			case err != nil:
				result.Failed[tenant] = err
			case changed:
				result.Changed++
			}
		}

		if len(tenants) < settingsPage {
			err := s.uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
				var err error
				result.Candidates, err = tx.Participants().Count(ctx, participant.Page{
					Stage: participant.StageCandidate,
				})
				if err != nil {
					return err
				}
				result.Queue, err = tx.Participants().Count(ctx, participant.Page{Queue: true})
				return err
			})
			return result, err
		}
	}
}

// SyncOne сверяет одного участника; true — видимое поменялось.
func (s *Settings) SyncOne(ctx context.Context, tenantID ids.TenantID) (bool, error) {
	settings, err := s.platform.MarketplaceSettings(ctx, tenantID)
	var invalid error
	if errors.Is(err, ports.ErrInvalidSettings) {
		invalid = err
		settings, err = ports.ShopSettings{}, nil
	}
	if errors.Is(err, ports.ErrMarketplaceDisabled) {
		// сервер отказал магазину-неучастнику — участие выключено, субсидии нет
		settings, err = ports.ShopSettings{}, nil
	}
	if err != nil {
		return false, err
	}

	var changed bool

	err = s.uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		p, err := tx.Participants().Get(ctx, tenantID)
		if err != nil {
			return err
		}

		now := s.clock.Now()
		changed, err = p.ApplyShopSettings(
			settings.Subsidy,
			settings.FreeDeliveryFrom,
			settings.Delivery,
			now,
		)
		if err != nil {
			return err
		}

		if err := tx.Participants().Update(ctx, p); err != nil {
			return err
		}
		if !changed {
			return nil
		}

		return tx.Outbox().Enqueue(ctx, appcatalog.SyncTenantTask(tenantID, now))
	})

	return changed, errors.Join(err, invalid)
}

// HandleTask — внеочередная сверка из outbox. Участника уже нет — сверять нечего.
func (s *Settings) HandleTask(ctx context.Context, task ports.Task) error {
	var payload settingsTaskPayload
	if err := json.Unmarshal(task.Payload, &payload); err != nil {
		return ErrInvalidTask.Wrap(err)
	}
	if task.Kind != TaskSyncSettings || payload.TenantID != string(task.TenantID) {
		return ErrInvalidTask.WithMessage("задача не сверки настроек или магазин не совпадает")
	}

	_, err := s.SyncOne(ctx, task.TenantID)
	if errors.Is(err, participant.ErrNotFound) {
		return nil
	}

	return err
}
