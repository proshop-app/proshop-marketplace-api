package participant

import (
	"context"
	"strings"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

// SyncSettings фиксирует запрос сверки в outbox до ответа серверу.
func (s *Service) SyncSettings(ctx context.Context, tenant ids.TenantID) error {
	return s.uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		if _, err := tx.Participants().Get(ctx, tenant); err != nil {
			return err
		}
		return tx.Outbox().Enqueue(ctx, SyncSettingsTask(tenant, s.clock.Now()))
	})
}

// ModerateCard пишет решение вместе с изменением, чтобы журнал не расходился с карточкой.
func (s *Service) ModerateCard(
	ctx context.Context,
	tenant ids.TenantID,
	key string,
	hide bool,
	decision participant.Decision,
) (View, error) {
	decision.Note = strings.TrimSpace(decision.Note)
	var view View
	err := s.uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		p, err := tx.Participants().Get(ctx, tenant)
		if err != nil {
			return err
		}
		now := s.clock.Now()
		previous := p.State().StoreCard.Profile.FieldValue(key)
		var changed bool
		action := participant.ActionCardShow
		if hide {
			action = participant.ActionCardHide
			changed, err = p.HideCard(key, decision.Note, now)
		} else {
			changed, err = p.ShowCard(key, now)
		}
		if err != nil {
			return err
		}
		if changed {
			if err := tx.Participants().Update(ctx, p); err != nil {
				return err
			}
			if err := tx.ParticipantDecisions().Add(ctx, participant.DecisionRecord{
				ID:            s.ids.NewUUID(),
				TenantID:      tenant,
				Decision:      decision,
				Actor:         "operator",
				Action:        action,
				PreviousValue: previous,
				Value:         key,
				At:            now,
			}); err != nil {
				return err
			}
			if err := tx.StoreCards().UpdateProfile(ctx, p); err != nil {
				return err
			}
			if err := tx.Outbox().Enqueue(ctx, ports.StoreCardTask(tenant, now)); err != nil {
				return err
			}
		}
		view, err = s.withTenant(ctx, tx, p)
		return err
	})
	return view, err
}
