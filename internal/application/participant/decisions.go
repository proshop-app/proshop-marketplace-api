package participant

import (
	"context"

	appcatalog "github.com/proshop-app/proshop-marketplace/internal/application/catalog"
	appmoderation "github.com/proshop-app/proshop-marketplace/internal/application/moderation"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

// DecisionPage сохраняет окно журнала вместе с общим числом записей.
type DecisionPage struct {
	Items  []participant.DecisionRecord
	Total  int
	Limit  int
	Offset int
}

// SetTrust не увеличивает версию при повторе того же уровня доверия.
func (s *Service) SetTrust(
	ctx context.Context,
	tenant ids.TenantID,
	trust string,
	decision participant.Decision,
) (View, error) {
	return s.decide(ctx, tenant, "trust", trust, decision)
}

func (s *Service) decide(
	ctx context.Context,
	tenant ids.TenantID,
	action string,
	value string,
	decision participant.Decision,
) (View, error) {
	var view View
	err := s.uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		p, err := tx.Participants().Get(ctx, tenant)
		if err != nil {
			return err
		}
		now := s.clock.Now()
		changed := false
		switch action {
		case "enable":
			changed = p.Enable(now)
		case "disable":
			changed = p.Disable(now)
		case "trust":
			changed, err = p.SetTrust(value, now)
		}
		if err != nil {
			return err
		}
		if changed {
			if err := tx.Participants().Update(ctx, p); err != nil {
				return err
			}
			record := participant.DecisionRecord{
				ID:       s.ids.NewUUID(),
				TenantID: tenant,
				Decision: decision,
				Action:   action,
				Value:    value,
				At:       now,
			}
			if err := tx.ParticipantDecisions().Add(ctx, record); err != nil {
				return err
			}
			if err := tx.Outbox().Enqueue(ctx, appcatalog.SyncTenantTask(tenant, now)); err != nil {
				return err
			}
			if action == "trust" {
				if err := tx.Outbox().Enqueue(ctx, appmoderation.ReevaluateTask(tenant, now)); err != nil {
					return err
				}
			}
		}
		view, err = withTenant(ctx, tx, p)
		return err
	})
	return view, err
}

// ListDecisions ограничивает объём истории участника.
func (s *Service) ListDecisions(ctx context.Context, tenant ids.TenantID, page participant.Page) (DecisionPage, error) {
	if page.Limit <= 0 || page.Limit > MaxPageLimit {
		page.Limit = 50
	}
	if page.Offset < 0 {
		page.Offset = 0
	}
	result := DecisionPage{Limit: page.Limit, Offset: page.Offset}
	err := s.uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		var err error
		result.Items, result.Total, err = tx.ParticipantDecisions().List(ctx, tenant, page)
		return err
	})
	return result, err
}
