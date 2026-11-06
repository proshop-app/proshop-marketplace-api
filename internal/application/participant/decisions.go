package participant

import (
	"context"
	"strings"
	"unicode/utf8"

	appcatalog "github.com/proshop-app/proshop-marketplace/internal/application/catalog"
	appmoderation "github.com/proshop-app/proshop-marketplace/internal/application/moderation"
	"github.com/proshop-app/proshop-marketplace/internal/application/moderationstatus"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/moderation"
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
	decision.Note = strings.TrimSpace(decision.Note)
	if utf8.RuneCountInString(decision.Note) > 2000 || (action != "enable" && decision.Note == "") {
		return View{}, participant.ErrInvalidNote
	}
	var view View
	err := s.uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		p, err := tx.Participants().Get(ctx, tenant)
		if err != nil {
			return err
		}
		message, err := moderation.ResolveReason(ctx, tx.ModerationRules(), decision.Reason)
		if err != nil {
			return err
		}
		decision.ReasonMessage = message
		previous := p.State().Trust
		if action != "trust" {
			previous = string(p.Stage())
		}
		now := s.clock.Now()
		changed := false
		switch action {
		case "enable":
			changed, err = p.Enable(now)
		case "disable":
			if p.Stage() != participant.StageConnected && p.Stage() != participant.StageDisconnected {
				return participant.ErrConflict
			}
			changed = p.Disable(now)
		case "return":
			changed, err = p.Return(now)
		case "trust":
			changed, err = p.SetTrust(value, now)
		}
		if err != nil {
			return err
		}
		if changed {
			record := participant.DecisionRecord{
				ID:            s.ids.NewUUID(),
				TenantID:      tenant,
				Decision:      decision,
				Actor:         "operator",
				Action:        action,
				Value:         value,
				PreviousValue: previous,
				At:            now,
			}
			if action != "trust" {
				p.RecordDecision(record)
			}
			if err := tx.Participants().Update(ctx, p); err != nil {
				return err
			}
			if err := tx.ParticipantDecisions().Add(ctx, record); err != nil {
				return err
			}
			if err := tx.Outbox().Enqueue(ctx, appcatalog.SyncTenantTask(tenant, now)); err != nil {
				return err
			}
			if err := tx.Outbox().Enqueue(ctx, ports.StoreCardTask(tenant, now)); err != nil {
				return err
			}
			if err := tx.Outbox().Enqueue(ctx, moderationstatus.ParticipantTask(tenant, now)); err != nil {
				return err
			}
			if action == "trust" {
				if err := tx.Outbox().Enqueue(ctx, appmoderation.ReevaluateTask(tenant, now)); err != nil {
					return err
				}
			}
		}
		view, err = s.withTenant(ctx, tx, p)
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
		if _, err := tx.Participants().Get(ctx, tenant); err != nil {
			return err
		}
		var err error
		result.Items, result.Total, err = tx.ParticipantDecisions().List(ctx, tenant, page)
		return err
	})
	return result, err
}
