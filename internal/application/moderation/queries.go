package moderation

import (
	"context"
	"errors"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/moderation"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

// CasePage сохраняет параметры окна очереди вместе с общим числом дел.
type CasePage struct {
	Items  []moderation.CaseState
	Total  int
	Limit  int
	Offset int
}

// VerdictPage сохраняет параметры окна и общее число вердиктов.
type VerdictPage struct {
	Items  []moderation.VerdictState
	Total  int
	Limit  int
	Offset int
}

// OfferView объединяет текущую оценку с открытым делом и историей решений.
type OfferView struct {
	Verdict *moderation.VerdictState
	Case    *moderation.CaseState
	History []moderation.Decision
}

// GetCase возвращает версию для следующего решения оператора.
func (s *Service) GetCase(ctx context.Context, id ids.UUID) (moderation.CaseState, error) {
	var result moderation.CaseState
	err := s.uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		c, err := tx.ModerationCases().Get(ctx, id)
		if err == nil {
			result = c.State()
		}
		return err
	})
	return result, err
}

// ListCases ограничивает размер очереди и сохраняет порядок открытия.
func (s *Service) ListCases(ctx context.Context, f moderation.Filter) (CasePage, error) {
	f = normalizeFilter(f)
	result := CasePage{Limit: f.Limit, Offset: f.Offset}
	err := s.uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		var err error
		result.Items, result.Total, err = tx.ModerationCases().List(ctx, f)
		return err
	})
	return result, err
}

// ListVerdicts ограничивает выборку результатов оценки.
func (s *Service) ListVerdicts(ctx context.Context, f moderation.Filter) (VerdictPage, error) {
	f = normalizeFilter(f)
	result := VerdictPage{Limit: f.Limit, Offset: f.Offset}
	err := s.uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		var err error
		result.Items, result.Total, err = tx.ModerationVerdicts().List(ctx, f)
		return err
	})
	return result, err
}

func normalizeFilter(f moderation.Filter) moderation.Filter {
	if f.Limit <= 0 || f.Limit > 200 {
		f.Limit = 50
	}
	if f.Offset < 0 {
		f.Offset = 0
	}
	return f
}

// GetOffer читает историю и текущую оценку в одной транзакции.
func (s *Service) GetOffer(ctx context.Context, offer ids.OfferID) (OfferView, error) {
	var result OfferView
	err := s.uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		if _, err := tx.Products().Get(ctx, offer.Tenant, offer.Product); err != nil {
			return err
		}
		v, err := tx.ModerationVerdicts().Get(ctx, offer.Tenant, offer.Product)
		if err != nil && !errors.Is(err, moderation.ErrNotFound) {
			return err
		}
		if v != nil {
			state := v.State()
			result.Verdict = &state
		}
		c, err := tx.ModerationCases().Open(ctx, offer.Tenant, offer.Product)
		if err != nil && !errors.Is(err, moderation.ErrNotFound) {
			return err
		}
		if c != nil {
			state := c.State()
			result.Case = &state
		}
		result.History, err = tx.ModerationDecisions().List(ctx, offer.Tenant, offer.Product)
		return err
	})
	return result, err
}

// ListStopPhrases читает фразы вместе с текущей ревизией правил.
func (s *Service) ListStopPhrases(ctx context.Context) ([]moderation.StopPhrase, error) {
	var rules moderation.Rules
	err := s.uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		var err error
		rules, err = tx.ModerationRules().Current(ctx)
		return err
	})
	return rules.Phrases, err
}

// AddStopPhrase ставит переоценку участников в транзакции изменения правил.
func (s *Service) AddStopPhrase(ctx context.Context, text string, actor moderation.Actor) error {
	phrase, err := moderation.NewStopPhrase(text, actor, s.clock.Now())
	if err != nil {
		return err
	}
	return s.uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		if err := tx.ModerationRules().AddPhrase(ctx, phrase); err != nil {
			return err
		}
		return EnqueueAll(ctx, tx, s.clock.Now())
	})
}

// RemoveStopPhrase не ставит повторную переоценку при отсутствии фразы.
func (s *Service) RemoveStopPhrase(ctx context.Context, text string) error {
	return s.uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		changed, err := tx.ModerationRules().RemovePhrase(ctx, moderation.Key(text))
		if err != nil || !changed {
			return err
		}
		return EnqueueAll(ctx, tx, s.clock.Now())
	})
}

// ListReasons возвращает операторский словарь без встроенных тематик.
func (s *Service) ListReasons(ctx context.Context) ([]moderation.ReasonCode, error) {
	var result []moderation.ReasonCode
	err := s.uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		var err error
		result, err = tx.ModerationRules().ListReasons(ctx)
		return err
	})
	return result, err
}

// PutReason проверяет код и текст до записи словаря.
func (s *Service) PutReason(ctx context.Context, reason moderation.ReasonCode) error {
	if err := reason.Validate(); err != nil {
		return err
	}
	return s.uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		return tx.ModerationRules().PutReason(ctx, reason)
	})
}
