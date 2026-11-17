package moderation

import (
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/domain/moderation"
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

// evaluationInput содержит значения, а не репозитории. Approval — уже прочитанное
// доказательство прежнего допуска; решение не может само дойти до хранилища.
type evaluationInput struct {
	Product     *catalog.Product
	Participant *participant.Participant
	Definition  taxonomy.Definition
	Policy      string
	Rules       moderation.Rules
	Verdict     *moderation.Verdict
	Case        *moderation.Case
	Signals     *catalog.Signals
	Approval    *moderation.VerdictState
	CaseID      ids.UUID
	Now         time.Time
}

type evaluationDecision struct {
	Verdict      *moderation.Verdict
	Case         *moderation.Case
	Observations []func()
	Changed      bool
}

// decideVerdict и decideCase не имеют ни Tx, ни Service: только доменные переходы.
// Два шага позволяют событию не читать дело и сигналы для неизменившегося допуска.
func decideVerdict(in evaluationInput) evaluationDecision {
	result := evaluationDecision{}
	if !evaluationPublished(in) || in.Participant == nil {
		return result
	}
	p := in.Product.State()
	verdict := moderation.NewVerdict(p.TenantID, p.ProductID)
	if in.Verdict != nil {
		verdict = moderation.RestoreVerdict(in.Verdict.State())
	}
	changed := false
	if in.Approval != nil {
		changed = verdict.CarryForward(*in.Approval, p.Revision, in.Definition.Version, in.Now)
	}
	if !changed {
		changed = verdict.Evaluate(p.Revision, in.Definition.Version, in.Rules, evaluationRulesInput(in), in.Now)
	}
	if changed {
		result.Verdict = verdict
		result.Changed = true
	}
	return result
}

func decideCase(in evaluationInput, result evaluationDecision) evaluationDecision {
	if !evaluationNeedsCase(in, result) {
		return result
	}
	var review *moderation.Case
	if in.Case != nil {
		review = moderation.RestoreCase(in.Case.State())
	}
	if !evaluationPublished(in) {
		if review != nil {
			// Снятие публикации закрывает дело, сохраняя прежнее решение оператора.
			review.Supersede(in.Now)
			result.Case, result.Changed = review, true
		}
		return result
	}
	verdict := effectiveVerdict(in, result).State()
	var card *moderation.ReviewCard
	if evaluationNeedsReview(in, result) {
		state := ports.OfferState{
			Product:     in.Product,
			Participant: in.Participant,
			Signals:     in.Signals,
			Verdict:     effectiveVerdict(in, result),
		}
		value := reviewCardFromState(state, in.Definition)
		card = &value
	}
	if review != nil {
		if review.Refresh(verdict, in.Now, card) {
			result.Case = review
		}
	} else if verdict.Status == moderation.Pending {
		result.Case = moderation.NewCase(in.CaseID, verdict, moderation.Rule, "", in.Now, card)
	}
	result.Changed = result.Verdict != nil || result.Case != nil
	return result
}

func evaluationPublished(in evaluationInput) bool {
	p := in.Product.State()
	return !p.Deleted && p.Card.Marketplace != nil && p.Card.Publication.Published
}

func effectiveVerdict(in evaluationInput, result evaluationDecision) *moderation.Verdict {
	if result.Verdict != nil {
		return result.Verdict
	}
	return in.Verdict
}

func evaluationNeedsCase(in evaluationInput, result evaluationDecision) bool {
	if !evaluationPublished(in) {
		return true
	}
	v := effectiveVerdict(in, result)
	return in.Participant != nil && v != nil && (result.Verdict != nil || v.State().Status == moderation.Pending)
}

func evaluationNeedsReview(in evaluationInput, result evaluationDecision) bool {
	if !evaluationPublished(in) || !evaluationNeedsCase(in, result) {
		return false
	}
	v := effectiveVerdict(in, result).State()
	return in.Case == nil && v.Status == moderation.Pending || in.Case != nil && in.Case.State().Revision != v.Revision
}

func evaluationRulesInput(in evaluationInput) moderation.Input {
	p := in.Product.State()
	return moderation.Input{
		Policy:      in.Policy,
		Name:        p.Card.Name,
		Description: p.Card.Description,
		Image:       p.Card.Image,
		Probation:   in.Participant != nil && in.Participant.State().Trust == participant.Probation,
	}
}

// completeEvaluation добавляет только идентификатор нового дела и отложенное наблюдение.
// Решения выше остаются вычислениями без внешних вызовов.
func (s *Service) completeEvaluation(in evaluationInput, result evaluationDecision) evaluationDecision {
	if evaluationNeedsReview(in, result) && in.Case == nil {
		in.CaseID = s.ids.NewUUID()
	}
	result = decideCase(in, result)
	if result.Verdict != nil && s.config.Metrics != nil {
		state := result.Verdict.State()
		result.Observations = append(result.Observations, func() { s.config.Metrics.Verdict(state) })
	}
	return result
}
