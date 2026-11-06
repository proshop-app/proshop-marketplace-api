package participant_test

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/proshop-app/proshop-marketplace/internal/domain/operator"
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
)

func TestCandidateStages(t *testing.T) {
	p, err := participant.New(activeTenant(t, participant.TenantActive), "Магазин", t0)
	if err != nil {
		t.Fatal(err)
	}
	if p.Stage() != participant.StageCandidate || !p.State().AwaitingReview || p.Enabled() || p.InQueue() {
		t.Fatal(p.State())
	}
	if _, err := p.Submit(t0); !errors.Is(err, participant.ErrConflict) {
		t.Fatal(err)
	}
	if _, err := p.Return(t0); err != nil {
		t.Fatal(err)
	}
	if p.Stage() != participant.StageReturned || p.State().AwaitingReview || p.Enabled() {
		t.Fatal(p.State())
	}
	if _, err := p.Submit(t0); err != nil {
		t.Fatal(err)
	}
	if p.Stage() != participant.StageCandidate || !p.State().AwaitingReview {
		t.Fatal(p.State())
	}
	if _, err := p.Submit(t0); !errors.Is(err, participant.ErrConflict) {
		t.Fatal(err)
	}
	if _, err := p.ApplyStoreCard(participant.Profile{}, participant.CardReadiness{Ready: true}, nil, t0); err != nil {
		t.Fatal(err)
	}
	if changed, err := p.Enable(t0); err != nil || !changed ||
		p.Stage() != participant.StageConnected || !p.Enabled() || p.State().AwaitingReview {
		t.Fatal(p.State())
	}
	if _, err := p.Submit(t0); !errors.Is(err, participant.ErrConflict) {
		t.Fatal(err)
	}
	if !p.Disable(t0) || p.Stage() != participant.StageDisconnected || p.Enabled() {
		t.Fatal(p.State())
	}
	if _, err := p.Submit(t0); !errors.Is(err, participant.ErrConflict) {
		t.Fatal(err)
	}
}

func TestLastDecisionIsValueSnapshot(t *testing.T) {
	p, err := participant.New(activeTenant(t, participant.TenantActive), "Магазин", t0)
	if err != nil {
		t.Fatal(err)
	}
	p.RecordDecision(participant.DecisionRecord{
		Action: "return",
		At:     t1,
		Decision: participant.Decision{
			Reason:        "r1",
			ReasonMessage: "Причина",
			Note:          "Исправьте",
			Operator:      operator.Principal{ID: "op1", Name: "Оператор"},
		},
	})
	state := p.State()
	raw, err := json.Marshal(state.LastDecision)
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]json.RawMessage
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"reason", "reasonMessage", "note", "at", "operator"} {
		if _, ok := document[key]; !ok {
			t.Fatalf("нет json-поля %s: %s", key, raw)
		}
	}
	if len(document) != 5 {
		t.Fatalf("реквизиты журнала попали в последнее решение: %s", raw)
	}
	state.LastDecision.Note = "Изменено"
	restored := participant.Restore(state)
	state.LastDecision.Note = "Снова изменено"
	if p.State().LastDecision.Note != "Исправьте" || restored.State().LastDecision.Note != "Изменено" {
		t.Fatal("снимок разделяет изменяемое решение с агрегатом")
	}
}
