package moderation

import (
	"errors"
	"reflect"
	"testing"

	"github.com/proshop-app/proshop-marketplace/internal/domain/moderation"
)

func TestRecoveryApprovalRequiresCompleteImmutableHistory(t *testing.T) {
	approved := recoveryHistoryState(1)
	approved.DecidedBy, approved.Status = moderation.Operator, moderation.Allowed
	middle, current := recoveryHistoryState(2), recoveryHistoryState(3)
	tests := []struct {
		name    string
		history []moderation.VerdictState
		valid   bool
	}{
		{name: "complete", history: []moderation.VerdictState{current, middle, approved}, valid: true},
		{name: "missing origin", history: []moderation.VerdictState{current, middle}},
		{name: "missing current", history: []moderation.VerdictState{middle, approved}},
		{name: "gap", history: []moderation.VerdictState{current, approved, recoveryHistoryState(0)}},
		{name: "duplicate", history: []moderation.VerdictState{current, middle, middle, approved}},
		{name: "future", history: []moderation.VerdictState{recoveryHistoryState(4), current, middle, approved}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := findRecoveryApproval(test.history, current, 1)
			if test.valid {
				if err != nil || !reflect.DeepEqual(got, approved) {
					t.Fatal("полная история не принята", got, err)
				}
			} else if !errors.Is(err, moderation.ErrCaseStale) {
				t.Fatal("неполная история принята", got, err)
			}
		})
	}
	for _, edit := range []func(*moderation.VerdictState){
		func(v *moderation.VerdictState) { v.DecidedBy = moderation.Operator },
		func(v *moderation.VerdictState) { v.Status = moderation.Rejected },
		func(v *moderation.VerdictState) { v.ManualReject = true },
		func(v *moderation.VerdictState) { v.Reasons[0].Key = "moderation.manual_review" },
		func(v *moderation.VerdictState) { v.TenantID = "t2" },
	} {
		changed := recoveryHistoryState(2)
		edit(&changed)
		_, err := findRecoveryApproval([]moderation.VerdictState{current, changed, approved}, current, 1)
		if !errors.Is(err, moderation.ErrCaseStale) {
			t.Fatal("последующее вмешательство или другой контекст приняты", changed, err)
		}
	}
	changed := current
	changed.RulesRevision++
	_, err := findRecoveryApproval([]moderation.VerdictState{changed, middle, approved}, current, 1)
	if !errors.Is(err, moderation.ErrCaseStale) {
		t.Fatal("текущее состояние расходится с неизменяемым журналом", err)
	}
}

func recoveryHistoryState(version int64) moderation.VerdictState {
	return moderation.VerdictState{
		TenantID:  "t1",
		ProductID: "p1",
		Version:   version,
		DecidedBy: moderation.Rule,
		Status:    moderation.Pending,
		Reasons:   []moderation.Reason{{Key: "moderation.probation"}},
	}
}
