package moderation

import (
	"reflect"
	"slices"
	"time"

	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

// VerdictState сохраняет основания оценки и версии её источников.
type VerdictState struct {
	TenantID        ids.TenantID  `json:"tenant_id"`
	ProductID       ids.ProductID `json:"product_id"`
	Revision        int64         `json:"revision"`
	RulesRevision   int64         `json:"rules_revision"`
	TaxonomyVersion int64         `json:"taxonomy_version"`
	Status          string        `json:"status"`
	Reasons         []Reason      `json:"reasons"`
	DecidedBy       string        `json:"decided_by"`
	ManualReject    bool          `json:"manual_reject"`
	Version         int64         `json:"version"`
	UpdatedAt       time.Time     `json:"updated_at"`
}

// Verdict защищает ручные решения от автоматической переоценки.
type Verdict struct {
	state  VerdictState
	origin VerdictState
}

// NewVerdict создаёт несохранённый результат для одного предложения.
func NewVerdict(tenant ids.TenantID, product ids.ProductID) *Verdict {
	return &Verdict{state: VerdictState{TenantID: tenant, ProductID: product}}
}

// RestoreVerdict восстанавливает исходную версию для условного обновления.
func RestoreVerdict(s VerdictState) *Verdict {
	s.Reasons = slices.Clone(s.Reasons)
	return &Verdict{state: s, origin: s}
}

// State отделяет снимок от изменяемого состояния агрегата.
func (v *Verdict) State() VerdictState {
	s := v.state
	s.Reasons = slices.Clone(s.Reasons)
	return s
}

// Origin сохраняет прочитанную версию для обнаружения конкурирующей записи.
func (v *Verdict) Origin() VerdictState { return RestoreVerdict(v.origin).State() }

// MarkSaved начинает новый цикл изменений после успешного сохранения.
func (v *Verdict) MarkSaved() { v.origin = v.State() }

// Allowed закрывает доступ, если оценка ещё не выполнялась.
func (v *Verdict) Allowed() bool { return v != nil && v.state.Status == Allowed }

// Evaluate сохраняет порядок правил и защищает решение оператора текущей ревизии.
func (v *Verdict) Evaluate(revision, taxonomy int64, rules Rules, in Input, now time.Time) bool {
	if revision < v.state.Revision || (revision == v.state.Revision && v.state.DecidedBy == Operator) {
		return false
	}
	in.ManualReject = v.state.ManualReject
	status, reasons := rules.Evaluate(in)
	if revision == v.state.Revision && taxonomy == v.state.TaxonomyVersion &&
		rules.Revision == v.state.RulesRevision && status == v.state.Status &&
		reflect.DeepEqual(reasons, v.state.Reasons) {
		return false
	}
	v.state.Revision = revision
	v.state.TaxonomyVersion = taxonomy
	v.state.RulesRevision = rules.Revision
	v.state.Status = status
	v.state.Reasons = reasons
	v.state.DecidedBy = Rule
	v.touch(now)
	return true
}

// Review скрывает предложение на время инициированной оператором проверки.
func (v *Verdict) Review(revision int64, now time.Time) bool {
	if revision < v.state.Revision ||
		(revision == v.state.Revision && v.state.Status == Pending && v.state.DecidedBy == Operator) {
		return false
	}
	v.state.Revision = revision
	v.state.Status = Pending
	v.state.DecidedBy = Operator
	v.state.Reasons = []Reason{{Key: "moderation.manual_review", Message: "проверка оператором"}}
	v.touch(now)
	return true
}

// Decide фиксирует решение для текущей ревизии.
func (v *Verdict) Decide(action, message string, now time.Time) bool {
	if action != "allow" && action != "reject" {
		return false
	}
	next := Allowed
	reasons := []Reason{}
	if action == "reject" {
		next = Rejected
		reasons = []Reason{{Key: "moderation.rejected_by_operator", Message: message}}
	}
	if v.state.DecidedBy == Operator && v.state.Status == next && reflect.DeepEqual(v.state.Reasons, reasons) {
		return false
	}
	v.state.DecidedBy = Operator
	v.state.ManualReject = action == "reject"
	v.state.Status = next
	v.state.Reasons = reasons
	v.touch(now)
	return true
}

func (v *Verdict) touch(now time.Time) {
	v.state.Version = v.origin.Version + 1
	v.state.UpdatedAt = now
}
