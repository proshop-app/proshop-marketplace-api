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

// Settings переносит настройки и серверную готовность одной транзакцией.
// Из карточки в документ предложения входит имя: только его смена ставит переиндексацию.
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

// SyncOne сверяет одного участника; true — сохранённые настройки изменились.
func (s *Settings) SyncOne(ctx context.Context, tenantID ids.TenantID) (bool, error) {
	settings, err := s.platform.MarketplaceSettings(ctx, tenantID)
	validCard := err == nil
	var invalid error
	if errors.Is(err, ports.ErrInvalidSettings) {
		invalid = err
		settings, err = ports.ShopSettings{}, nil
	}
	if errors.Is(err, ports.ErrMarketplaceDisabled) {
		// Отказ обнуляет доставку; карточка и решения оператора сохраняются.
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
		oldName := p.Name()
		cardBefore := p.State().StoreCard
		hiddenBefore := cardBefore.Hidden
		shopChanged, err := p.ApplyShopSettings(
			settings.Subsidy,
			settings.FreeDeliveryFrom,
			settings.Delivery,
			now,
		)
		if err != nil {
			return err
		}

		changed = shopChanged
		var cardChanged bool
		if validCard {
			cardChanged, err = p.ApplyStoreCard(settings.Profile, settings.Card, settings.Returns, now)
		}
		if err != nil {
			return err
		}
		changed = changed || cardChanged

		remaining := p.State().StoreCard.Hidden
		for _, hidden := range hiddenBefore {
			stillHidden := false
			for _, current := range remaining {
				stillHidden = stillHidden || current.Key == hidden.Key
			}
			if stillHidden {
				continue
			}
			if err := tx.ParticipantDecisions().Add(ctx, participant.DecisionRecord{
				ID:            s.ids.NewUUID(),
				TenantID:      tenantID,
				Actor:         participant.ActorSystem,
				Action:        participant.ActionCardShow,
				Value:         hidden.Key,
				PreviousValue: cardBefore.Profile.FieldValue(hidden.Key),
				Decision:      participant.Decision{Note: "Значение пункта изменилось при сверке"},
				At:            now,
			}); err != nil {
				return err
			}
		}

		// Время сверки сохраняется и без изменений: по нему оператор видит, что сервер отвечает.
		if err := tx.Participants().Update(ctx, p); err != nil {
			return err
		}
		if !changed {
			return nil
		}
		if err := tx.Outbox().Enqueue(ctx, ports.StoreCardTask(tenantID, now)); err != nil {
			return err
		}
		if !shopChanged && oldName == p.Name() {
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
