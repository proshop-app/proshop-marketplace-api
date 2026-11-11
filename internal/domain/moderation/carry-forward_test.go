package moderation_test

import (
	"reflect"
	"testing"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/domain/moderation"
)

func TestCarryForwardPreservesDecisionAndRecordsCurrentTime(t *testing.T) {
	for _, recovery := range []bool{false, true} {
		t.Run(map[bool]string{false: "delivery", true: "recovery"}[recovery], func(t *testing.T) {
			approved := carriedApproval()
			verdict := moderation.RestoreVerdict(approved)
			now := approved.UpdatedAt.Add(time.Hour)
			if recovery {
				verdict.Evaluate(17, 12, moderation.Rules{Revision: 4}, moderation.Input{Probation: true}, now)
				verdict.MarkSaved()
			}
			origin := verdict.State()
			if !verdict.CarryForward(approved, 17, 12, now) {
				t.Fatal("подтверждённое одобрение не перенесено")
			}
			state := verdict.State()
			if state.Status != moderation.Allowed || state.DecidedBy != moderation.Operator || state.ManualReject ||
				state.Revision != 17 || state.TaxonomyVersion != 12 || state.RulesRevision != approved.RulesRevision ||
				state.Operator != approved.Operator || !reflect.DeepEqual(state.Decision, approved.Decision) ||
				!state.UpdatedAt.Equal(now) || state.Version != origin.Version+1 || len(state.Reasons) != 0 {
				t.Fatalf("неверные основания переноса: %+v", state)
			}
			if !state.Decision.At.Equal(approved.Decision.At) || !verdict.Origin().UpdatedAt.Equal(origin.UpdatedAt) {
				t.Fatal("перенос заменил историческое время решения или origin")
			}
			verdict.MarkSaved()
			if verdict.CarryForward(approved, 17, 12, now.Add(time.Hour)) ||
				!reflect.DeepEqual(state, verdict.State()) {
				t.Fatal("повтор переноса изменил вердикт")
			}
			approved.Decision.Note = "Изменено"
			approved.Decision.Reason.Text = "Изменено"
			if verdict.State().Decision.Note != "Проверено" || verdict.State().Decision.Reason.Text != "Причина" {
				t.Fatal("перенесённое решение разделяет изменяемые данные с исходным")
			}
		})
	}
}

func TestCarryForwardRejectsUnprovenOrSupersededApproval(t *testing.T) {
	tests := []struct {
		name     string
		edit     func(*moderation.VerdictState, *moderation.VerdictState)
		revision int64
		taxonomy int64
	}{
		{
			name: "rule approval",
			edit: func(approved, _ *moderation.VerdictState) { approved.DecidedBy = moderation.Rule },
		},
		{
			name: "rejected approval",
			edit: func(approved, _ *moderation.VerdictState) { approved.Status = moderation.Rejected },
		},
		{
			name: "pending approval",
			edit: func(approved, _ *moderation.VerdictState) { approved.Status = moderation.Pending },
		},
		{
			name: "missing decision",
			edit: func(approved, _ *moderation.VerdictState) { approved.Decision = nil },
		},
		{
			name: "reject decision",
			edit: func(approved, _ *moderation.VerdictState) { approved.Decision.Action = moderation.Reject },
		},
		{
			name: "missing actor",
			edit: func(approved, _ *moderation.VerdictState) { approved.Operator.ID = " " },
		},
		{
			name: "missing decision time",
			edit: func(approved, _ *moderation.VerdictState) { approved.Decision.At = time.Time{} },
		},
		{
			name: "manual rejection provenance",
			edit: func(approved, _ *moderation.VerdictState) { approved.ManualReject = true },
		},
		{
			name: "other tenant",
			edit: func(approved, _ *moderation.VerdictState) { approved.TenantID = "t2" },
		},
		{
			name: "other product",
			edit: func(approved, _ *moderation.VerdictState) { approved.ProductID = "p2" },
		},
		{
			name: "newer operator decision",
			edit: func(_, current *moderation.VerdictState) { current.Version++ },
		},
		{
			name: "different operator decision",
			edit: func(_, current *moderation.VerdictState) { current.Operator.ID = "o2" },
		},
		{
			name: "manual review",
			edit: func(_, current *moderation.VerdictState) { current.Status = moderation.Pending },
		},
		{
			name: "current rejection",
			edit: func(_, current *moderation.VerdictState) { current.Status = moderation.Rejected },
		},
		{
			name: "current sticky rejection",
			edit: func(_, current *moderation.VerdictState) { current.ManualReject = true },
		},
		{
			name: "current newer revision",
			edit: func(_, current *moderation.VerdictState) { current.Revision = 18 },
		},
		{
			name:     "not a new delivery",
			revision: 16,
		},
		{
			name:     "taxonomy downgrade",
			taxonomy: 2,
		},
		{
			name: "unrecorded approval",
			edit: func(approved, _ *moderation.VerdictState) { approved.Version = 0 },
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			approved := carriedApproval()
			current := moderation.RestoreVerdict(approved).State()
			if test.edit != nil {
				test.edit(&approved, &current)
			}
			verdict := moderation.RestoreVerdict(current)
			revision, taxonomy := int64(17), int64(12)
			if test.revision != 0 {
				revision = test.revision
			}
			if test.taxonomy != 0 {
				taxonomy = test.taxonomy
			}
			if verdict.CarryForward(approved, revision, taxonomy, current.UpdatedAt.Add(time.Hour)) ||
				!reflect.DeepEqual(current, verdict.State()) {
				t.Fatal("перенос изменил неподтверждённое или заменённое решение")
			}
		})
	}
}

func carriedApproval() moderation.VerdictState {
	now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	verdict := moderation.NewVerdict("t1", "p1")
	verdict.Evaluate(16, 3, moderation.Rules{Revision: 4}, moderation.Input{Probation: true}, now)
	verdict.MarkSaved()
	verdict.Decide(moderation.Allow, "", now)
	verdict.RecordDecision(moderation.Actor{ID: "o1", Name: "Оператор"}, moderation.VerdictDecision{
		Action: moderation.Allow,
		Reason: &moderation.ReasonCode{Code: "r1", Text: "Причина"},
		Note:   "Проверено",
		At:     now,
	})
	verdict.MarkSaved()
	return verdict.State()
}
