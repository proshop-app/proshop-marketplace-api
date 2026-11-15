package moderation_test

import (
	"context"
	"testing"

	appmoderation "github.com/proshop-app/proshop-marketplace/internal/application/moderation"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/moderation"
)

type evaluationCountingTx struct {
	ports.Tx
	caseReads int
	cardReads int
}

func (tx *evaluationCountingTx) ModerationCases() moderation.CaseRepository {
	tx.caseReads++
	return tx.Tx.ModerationCases()
}

func (tx *evaluationCountingTx) Offers() ports.OfferStates {
	tx.cardReads++
	return tx.Tx.Offers()
}

func TestUnchangedEvaluationQueries(t *testing.T) {
	for _, status := range []moderation.Status{moderation.Allowed, moderation.Rejected, moderation.Pending} {
		t.Run(string(status), func(t *testing.T) {
			e := setup()
			e.project(t, "p1", 1)
			ctx := context.Background()
			if status != moderation.Pending {
				c := e.view(t, "p1").Case
				decide := e.service.Allow
				if status == moderation.Rejected {
					decide = e.service.Reject
				}
				_, err := decide(ctx, appmoderation.DecisionCommand{
					CaseID:          c.ID,
					ExpectedVersion: c.Version,
					Note:            "Проверено",
				})
				if err != nil {
					t.Fatal(err)
				}
			}
			before := e.view(t, "p1")
			err := e.store.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
				product, err := tx.Products().Get(ctx, "t1", "p1")
				if err != nil {
					return err
				}
				counted := &evaluationCountingTx{Tx: tx}
				if err := e.service.Evaluate(ctx, counted, product, nil, nil, e.clock.Now()); err != nil {
					return err
				}
				wantCases := 0
				if status == moderation.Pending {
					wantCases = 1
				}
				if counted.caseReads != wantCases || counted.cardReads != 0 {
					t.Fatalf(
						"обращения к делам: %d, ожидалось %d; к карточке: %d",
						counted.caseReads,
						wantCases,
						counted.cardReads,
					)
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			after := e.view(t, "p1")
			if !samePublicationVerdict(before.Verdict, after.Verdict) {
				t.Fatal("повторная оценка изменила вердикт")
			}
			if before.Case != nil && (after.Case == nil || after.Case.Version != before.Case.Version) {
				t.Fatal("повторная оценка изменила дело")
			}
		})
	}
}
