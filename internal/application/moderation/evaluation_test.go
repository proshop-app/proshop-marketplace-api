package moderation

import (
	"reflect"
	"testing"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/domain/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/domain/moderation"
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
)

func TestDecisionDoesNotMutateLoadedVerdictOrCase(t *testing.T) {
	now := time.Date(2026, 11, 17, 0, 0, 0, 0, time.UTC)
	verdict := moderation.RestoreVerdict(moderation.VerdictState{
		TenantID:  "t1",
		ProductID: "p1",
		Revision:  1,
		Version:   1,
		Status:    moderation.Allowed,
	})
	opened := moderation.NewCase("case", verdict.State(), moderation.Rule, "", now, nil)
	previousCase := opened.State()
	previousVerdict := verdict.State()
	in := evaluationInput{
		Product: catalog.RestoreProduct(catalog.ProductState{
			TenantID:  "t1",
			ProductID: "p1",
			Revision:  2,
			Card: catalog.Card{
				Name:        "Товар",
				Marketplace: &taxonomy.Card{CategoryID: "c1", Published: true},
				Publication: taxonomy.Publication{Published: true, Version: 2},
			},
		}),
		Participant: participant.Restore(participant.State{TenantID: "t1", Trust: participant.Probation}),
		Definition:  taxonomy.Definition{Version: 2},
		Rules:       moderation.Rules{Revision: 2},
		Verdict:     verdict,
		Case:        opened,
		Now:         now,
	}
	decision := decideCase(in, decideVerdict(in))
	if !decision.Changed || decision.Verdict == nil || decision.Case == nil || decision.Case.State().Revision != 2 {
		t.Fatal("решение не содержит изменённые агрегаты", decision)
	}
	if !reflect.DeepEqual(verdict.State(), previousVerdict) || !reflect.DeepEqual(opened.State(), previousCase) {
		t.Fatal("вычисление изменило вход до пакетного сохранения")
	}
}
