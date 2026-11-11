package moderation

import (
	"reflect"
	"strings"
	"time"
)

// CarryForward сохраняет прежнее одобрение только после внешней проверки неизменности содержимого и политики.
// Это перенос основания, а не новое решение оператора: время решения остаётся историческим.
func (v *Verdict) CarryForward(approved VerdictState, revision, taxonomy int64, now time.Time) bool {
	if !validCarriedApproval(approved) || approved.TenantID != v.state.TenantID ||
		approved.ProductID != v.state.ProductID || revision <= approved.Revision || revision < v.state.Revision ||
		taxonomy < 1 || taxonomy < approved.TaxonomyVersion || taxonomy < v.state.TaxonomyVersion ||
		v.state.ManualReject || v.state.Version < 1 {
		return false
	}
	if v.state.DecidedBy == Operator {
		if v.state.Status != Allowed || v.state.Version != approved.Version || v.state.Revision != approved.Revision ||
			v.state.Operator != approved.Operator || !reflect.DeepEqual(v.state.Decision, approved.Decision) {
			return false
		}
	} else if v.state.DecidedBy != Rule || v.state.Status != Pending {
		return false
	}
	approval := RestoreVerdict(approved).State()
	v.state.Revision = revision
	v.state.TaxonomyVersion = taxonomy
	v.state.RulesRevision = approval.RulesRevision
	v.state.Status = Allowed
	v.state.Reasons = approval.Reasons
	v.state.DecidedBy = Operator
	v.state.ManualReject = false
	v.state.Operator = approval.Operator
	v.state.Decision = approval.Decision
	v.touch(now)
	return true
}

func validCarriedApproval(approved VerdictState) bool {
	return approved.DecidedBy == Operator && approved.Status == Allowed && !approved.ManualReject &&
		approved.Version > 0 && approved.Revision > 0 && strings.TrimSpace(approved.Operator.ID) != "" &&
		approved.Decision != nil && approved.Decision.Action == Allow && !approved.Decision.At.IsZero()
}
