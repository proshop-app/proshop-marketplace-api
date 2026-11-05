package moderation

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	appcatalog "github.com/proshop-app/proshop-marketplace/internal/application/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/application/moderationstatus"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/moderation"
	"github.com/proshop-app/proshop-marketplace/internal/domain/operator"
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

// DecisionTTL ограничивает срок воспроизведения первоначального ответа.
const DecisionTTL = 24 * time.Hour

// DecisionCommand требует версию дела, прочитанную оператором.
type DecisionCommand struct {
	IdempotencyKey  string
	CaseID          ids.UUID
	ExpectedVersion int64
	Operator        moderation.Actor
	Reason          string
	Note            string
}

// OpenCase скрывает предложение до решения оператора.
func (s *Service) OpenCase(
	ctx context.Context,
	offer ids.OfferID,
	note string,
) (moderation.CaseState, error) {
	if strings.TrimSpace(note) == "" || utf8.RuneCountInString(strings.TrimSpace(note)) > 2000 {
		return moderation.CaseState{}, moderation.ErrInvalid
	}
	var result moderation.CaseState
	err := s.uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		product, err := tx.Products().Get(ctx, offer.Tenant, offer.Product)
		if err != nil {
			return err
		}
		p := product.State()
		if p.Deleted || p.Card.Marketplace == nil {
			return moderation.ErrInvalid
		}
		existing, err := tx.ModerationCases().Open(ctx, offer.Tenant, offer.Product)
		if err == nil {
			return &moderation.CaseConflict{Current: existing.State()}
		}
		if !errors.Is(err, moderation.ErrNotFound) {
			return err
		}
		verdict, err := tx.ModerationVerdicts().Get(ctx, offer.Tenant, offer.Product)
		if errors.Is(err, moderation.ErrNotFound) {
			verdict, err = moderation.NewVerdict(offer.Tenant, offer.Product), nil
		}
		if err != nil {
			return err
		}
		now := s.clock.Now()
		verdict.Review(p.Revision, now)
		c := moderation.NewCase(s.ids.NewUUID(), verdict.State(), moderation.Operator, note, now)
		// Одинаковый порядок с Evaluate исключает взаимную блокировку дела и вердикта.
		if err := tx.ModerationVerdicts().Save(ctx, verdict); err != nil {
			return err
		}
		s.recordVerdict(tx, verdict.State())
		if err := tx.ModerationCases().Save(ctx, c); err != nil {
			return err
		}
		result = c.State()
		if err := tx.Outbox().Enqueue(ctx, moderationstatus.OfferTask(offer.Tenant, offer.Product, now)); err != nil {
			return err
		}
		return tx.Outbox().Enqueue(ctx, appcatalog.SyncOfferTask(offer.Tenant, offer.Product, now))
	})
	if errors.Is(err, moderation.ErrCaseConflict) || errors.Is(err, moderation.ErrConflict) {
		readErr := s.uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
			current, getErr := tx.ModerationCases().Open(ctx, offer.Tenant, offer.Product)
			if getErr != nil {
				return getErr
			}
			result = current.State()
			return nil
		})
		if readErr != nil {
			return result, readErr
		}
		err = &moderation.CaseConflict{Current: result}
	}
	return result, err
}

// Allow разрешает только проверенную ревизию карточки.
func (s *Service) Allow(ctx context.Context, cmd DecisionCommand) (moderation.CaseState, error) {
	return s.decide(ctx, cmd, moderation.Allow)
}

// Reject сохраняет ручной отказ для последующих ревизий.
func (s *Service) Reject(ctx context.Context, cmd DecisionCommand) (moderation.CaseState, error) {
	return s.decide(ctx, cmd, moderation.Reject)
}

func (s *Service) decide(
	ctx context.Context,
	cmd DecisionCommand,
	action moderation.Action,
) (moderation.CaseState, error) {
	identity, _ := json.Marshal([]string{cmd.Operator.ID, cmd.IdempotencyKey})
	key := fmt.Sprintf("%x", sha256.Sum256(identity))
	request, _ := json.Marshal([]any{cmd.CaseID, action, cmd.ExpectedVersion, cmd.Reason, cmd.Note})
	fingerprint := fmt.Sprintf("%x", sha256.Sum256(request))
	var result moderation.CaseState
	err := s.uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		if cmd.IdempotencyKey != "" {
			cached, found, err := tx.ModerationDecisions().Cached(ctx, key, s.clock.Now())
			if err != nil {
				return err
			}
			if found {
				if cached.Fingerprint != fingerprint {
					return moderation.ErrKeyReused
				}
				result = cached.Result
				return nil
			}
		}
		c, err := tx.ModerationCases().Get(ctx, cmd.CaseID)
		if err != nil {
			return err
		}
		state := c.State()
		p, err := tx.Products().Get(ctx, state.TenantID, state.ProductID)
		if err != nil {
			return err
		}
		now := s.clock.Now()
		if _, err := c.Decide(cmd.ExpectedVersion, p.State().Revision, action, cmd.Note, now); err != nil {
			return err
		}
		if p.State().Deleted || p.State().Card.Marketplace == nil {
			return moderation.ErrCaseStale
		}
		if err := checkReason(ctx, tx, cmd.Reason); err != nil {
			return err
		}
		verdict, err := tx.ModerationVerdicts().Get(ctx, state.TenantID, state.ProductID)
		if err != nil {
			return err
		}
		message := cmd.Reason
		if message == "" {
			message = strings.TrimSpace(cmd.Note)
		}
		verdict.Decide(action, message, now)
		// Одинаковый порядок с Evaluate исключает взаимную блокировку дела и вердикта.
		if err := tx.ModerationVerdicts().Save(ctx, verdict); err != nil {
			return err
		}
		s.recordVerdict(tx, verdict.State())
		if err := tx.ModerationCases().Save(ctx, c); err != nil {
			return err
		}
		decision := moderation.Decision{
			ID:       s.ids.NewUUID(),
			CaseID:   state.ID,
			Operator: cmd.Operator,
			Action:   action,
			Reason:   cmd.Reason,
			Note:     strings.TrimSpace(cmd.Note),
			Revision: state.Revision,
			At:       now,
		}
		if err := tx.ModerationDecisions().Add(ctx, decision); err != nil {
			return err
		}
		if action == moderation.Allow {
			if err := s.approve(ctx, tx, state.TenantID, cmd.Operator, now); err != nil {
				return err
			}
		}
		result = c.State()
		if s.config.Metrics != nil {
			tx.AfterCommit(func() { s.config.Metrics.Decision(action) })
		}
		if err := tx.Outbox().Enqueue(ctx, appcatalog.SyncOfferTask(state.TenantID, state.ProductID, now)); err != nil {
			return err
		}
		statusTask := moderationstatus.OfferTask(state.TenantID, state.ProductID, now)
		if err := tx.Outbox().Enqueue(ctx, statusTask); err != nil {
			return err
		}
		if cmd.IdempotencyKey != "" {
			return tx.ModerationDecisions().Cache(ctx, key, moderation.CachedDecision{
				Fingerprint: fingerprint,
				Result:      result,
				ExpiresAt:   now.Add(DecisionTTL),
			})
		}
		return nil
	})
	if errors.Is(err, moderation.ErrCaseConflict) || errors.Is(err, moderation.ErrConflict) {
		current, readErr := s.GetCase(ctx, cmd.CaseID)
		if readErr != nil {
			return result, readErr
		}
		err = &moderation.CaseConflict{Current: current}
	}
	return result, err
}

func checkReason(ctx context.Context, tx ports.Tx, code string) error {
	if code == "" {
		return nil
	}
	reasons, err := tx.ModerationRules().ListReasons(ctx)
	if err != nil {
		return err
	}
	for _, reason := range reasons {
		if reason.Code == code {
			return nil
		}
	}
	return moderation.ErrInvalid.WithMessage("неизвестный код причины")
}

func (s *Service) approve(
	ctx context.Context,
	tx ports.Tx,
	tenant ids.TenantID,
	actor moderation.Actor,
	now time.Time,
) error {
	p, err := tx.Participants().Get(ctx, tenant)
	if err != nil {
		return err
	}
	trust := p.State().Trust
	p.ApproveOffer(s.config.ProbationOffers, now)
	if err := tx.Participants().Update(ctx, p); err != nil {
		return err
	}
	if p.State().Trust != trust {
		record := participant.DecisionRecord{
			ID:       s.ids.NewUUID(),
			TenantID: tenant,
			Decision: participant.Decision{
				Operator: operator.Principal{ID: actor.ID, Name: actor.Name},
				Reason:   "probation_completed",
			},
			Action: "trust",
			Value:  participant.Trusted,
			At:     now,
		}
		if err := tx.ParticipantDecisions().Add(ctx, record); err != nil {
			return err
		}
		if err := tx.Outbox().Enqueue(ctx, moderationstatus.ParticipantTask(tenant, now)); err != nil {
			return err
		}
		return tx.Outbox().Enqueue(ctx, ReevaluateTask(tenant, now))
	}
	return nil
}
