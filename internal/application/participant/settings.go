package participant

import (
	"context"

	appcatalog "github.com/proshop-app/proshop-marketplace/internal/application/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

// settingsPage — участников за один проход сверки.
const settingsPage = 200

// SettingsResult — итог сверки настроек магазинов.
type SettingsResult struct {
	Checked int
	Changed int
	Failed  map[ids.TenantID]error
}

// Settings сверяет с сервером настройки магазинов-участников: флаг участия и
// субсидию доставки. Смена флага меняет видимость предложений — магазин
// ставится на переиндексацию в той же транзакции.
type Settings struct {
	uow      ports.UnitOfWork
	platform ports.PlatformAPI
	clock    ports.Clock
}

// NewSettings — сверка настроек магазинов.
func NewSettings(uow ports.UnitOfWork, platform ports.PlatformAPI, clock ports.Clock) *Settings {
	return &Settings{uow: uow, platform: platform, clock: clock}
}

// Sync сверяет всех участников. Сбой по одному магазину не останавливает остальных.
func (s *Settings) Sync(ctx context.Context) (SettingsResult, error) {
	result := SettingsResult{Failed: map[ids.TenantID]error{}}

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
			return result, nil
		}
	}
}

// SyncOne сверяет одного участника; true — видимое поменялось.
func (s *Settings) SyncOne(ctx context.Context, tenantID ids.TenantID) (bool, error) {
	settings, err := s.platform.MarketplaceSettings(ctx, tenantID)
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
		if changed, err = p.ApplyShopSettings(settings.Enabled, settings.Subsidy, now); err != nil {
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

	return changed, err
}
