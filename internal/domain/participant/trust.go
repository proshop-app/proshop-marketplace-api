package participant

import (
	"context"
	"fmt"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/domain/operator"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

// Значения статусов сохраняются в БД и проверяются ограничениями схемы.
const (
	Probation = "probation"
	Trusted   = "trusted"
)

// Decision хранит основание решения для последующего аудита.
type Decision struct {
	Operator      operator.Principal
	ReasonMessage string
	Reason        string
	Note          string
}

// DecisionRecord фиксирует действие и значение независимо от будущего состояния участника.
type DecisionRecord struct {
	ID       ids.UUID
	TenantID ids.TenantID
	Decision
	Actor  string
	Action string
	Value  string
	At     time.Time
}

// DecisionRepository разрешает только добавление и чтение журнала.
type DecisionRepository interface {
	Add(context.Context, DecisionRecord) error
	List(context.Context, ids.TenantID, Page) ([]DecisionRecord, int, error)
}

// SetTrust не увеличивает версию при повторе того же уровня доверия.
func (p *Participant) SetTrust(trust string, now time.Time) (bool, error) {
	if trust != Probation && trust != Trusted {
		return false, ErrInvalidTrust
	}
	if p.state.Trust == trust {
		return false, nil
	}
	p.state.Trust = trust
	if trust == Probation {
		p.state.ApprovedOffers = 0
	}
	p.touch(now)
	return true, nil
}

// ApproveOffer учитывает одобрение и завершает испытание при достижении порога.
func (p *Participant) ApproveOffer(threshold int, now time.Time) bool {
	p.state.ApprovedOffers++
	if p.state.Trust == Probation && p.state.ApprovedOffers >= threshold {
		p.state.Trust = Trusted
	}
	p.touch(now)
	return true
}

// PromotionDecision не приписывает автоматическое повышение последнему оператору.
func (p *Participant) PromotionDecision(id ids.UUID, now time.Time) DecisionRecord {
	return DecisionRecord{
		ID:       id,
		TenantID: p.state.TenantID,
		Actor:    "system",
		Action:   "trust",
		Value:    Trusted,
		Decision: Decision{Note: fmt.Sprintf("Одобрено %d предложений", p.state.ApprovedOffers)},
		At:       now,
	}
}
