package moderation_test

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	appcatalog "github.com/proshop-app/proshop-marketplace/internal/application/catalog"
	appmoderation "github.com/proshop-app/proshop-marketplace/internal/application/moderation"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/domain/moderation"
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
	"github.com/proshop-app/proshop-marketplace/pkg/money"
)

type revisionOnlyEvaluator struct {
	service *appmoderation.Service
}

func TestRecoverProjectionDryRunAndAtomicApply(t *testing.T) {
	e, cmd := lostProjectionApproval(t)
	before := e.view(t, "p1")
	productBefore, _ := e.store.Product("t1", "p1")
	memberBefore, _ := e.store.Participant("t1")
	tasksBefore := e.store.OutboxRows()
	preview, err := e.service.RecoverProjection(context.Background(), cmd)
	if err != nil || preview.Applied || preview.Status != moderation.Allowed || preview.ApprovedRevision != 1 ||
		preview.TargetRevision != 2 || preview.BeforeVerdictVersion != before.Verdict.Version ||
		preview.AfterVerdictVersion != before.Verdict.Version+1 || preview.Operator != before.History[0].Operator ||
		!preview.OriginalDecisionAt.Equal(before.History[0].At) {
		t.Fatal("неверный проверяемый результат", preview, err)
	}
	if !reflect.DeepEqual(e.view(t, "p1"), before) || !reflect.DeepEqual(e.store.OutboxRows(), tasksBefore) {
		t.Fatal("dry-run изменил вердикт, дело, историю или outbox")
	}
	cmd.Apply = true
	e.clock.Advance(time.Hour)
	applied, err := e.service.RecoverProjection(context.Background(), cmd)
	after := e.view(t, "p1")
	productAfter, _ := e.store.Product("t1", "p1")
	memberAfter, _ := e.store.Participant("t1")
	if err != nil || !applied.Applied || after.Verdict.Status != moderation.Allowed ||
		after.Verdict.DecidedBy != moderation.Operator || after.Verdict.Revision != 2 || after.Case != nil ||
		after.Verdict.Operator != before.History[0].Operator ||
		!after.Verdict.Decision.At.Equal(before.History[0].At) || !after.Verdict.UpdatedAt.Equal(e.clock.Now()) ||
		after.Verdict.Version != before.Verdict.Version+1 || !e.document(t).Visible {
		t.Fatal("восстановление не сохранило исходное решение", applied, after, err)
	}
	closed, err := e.service.GetCase(context.Background(), before.Case.ID)
	if err != nil || closed.Status != moderation.Superseded || closed.DecidedAt == nil ||
		!closed.DecidedAt.Equal(e.clock.Now()) {
		t.Fatal("текущее автоматическое дело не закрыто атомарно", closed, err)
	}
	if !reflect.DeepEqual(productBefore, productAfter) || !reflect.DeepEqual(memberBefore, memberAfter) ||
		!reflect.DeepEqual(before.History, after.History) || len(after.Verdicts) != len(before.Verdicts)+1 {
		t.Fatal("восстановление изменило source, счётчик одобрений или создало операторское решение")
	}
	tasksAfter := e.store.OutboxRows()
	_, err = e.service.RecoverProjection(context.Background(), cmd)
	if !errors.Is(err, moderation.ErrConflict) || !reflect.DeepEqual(e.view(t, "p1"), after) ||
		!reflect.DeepEqual(e.store.OutboxRows(), tasksAfter) {
		t.Fatal("повторный CAS имел побочные эффекты", err)
	}
}

func TestRecoverProjectionRejectsUnprovenSourceAndStaleCAS(t *testing.T) {
	tests := []struct {
		name string
		edit func(*appmoderation.RecoverProjectionCommand)
	}{
		{name: "revision", edit: func(cmd *appmoderation.RecoverProjectionCommand) { cmd.ExpectedRevision++ }},
		{name: "verdict", edit: func(cmd *appmoderation.RecoverProjectionCommand) { cmd.ExpectedVerdictVersion++ }},
		{name: "approval", edit: func(cmd *appmoderation.RecoverProjectionCommand) { cmd.ApprovedVerdictVersion++ }},
		{name: "nonadjacent proof", edit: func(cmd *appmoderation.RecoverProjectionCommand) {
			cmd.Current.Revision, cmd.ExpectedRevision = 3, 3
		}},
		{name: "stock", edit: func(cmd *appmoderation.RecoverProjectionCommand) { cmd.Previous.Available++ }},
		{name: "price", edit: func(cmd *appmoderation.RecoverProjectionCommand) {
			cmd.Previous.Price = money.FromKopecks(200)
		}},
		{name: "assets", edit: func(cmd *appmoderation.RecoverProjectionCommand) {
			cmd.Previous.Card.Assets[0] = "asset-2"
		}},
		{name: "both proofs differ from saved source", edit: func(cmd *appmoderation.RecoverProjectionCommand) {
			cmd.Previous.Card.SKU, cmd.Current.Card.SKU = "sku-2", "sku-2"
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			e, cmd := lostProjectionApproval(t)
			before, tasks := e.view(t, "p1"), e.store.OutboxRows()
			cmd.Apply = true
			test.edit(&cmd)
			if _, err := e.service.RecoverProjection(context.Background(), cmd); err == nil {
				t.Fatal("недоказанный перенос принят")
			}
			if !reflect.DeepEqual(e.view(t, "p1"), before) || !reflect.DeepEqual(e.store.OutboxRows(), tasks) {
				t.Fatal("отказавшая проверка изменила состояние")
			}
		})
	}
}

func TestRecoverProjectionDoesNotCrossLaterOperatorActions(t *testing.T) {
	for _, action := range []moderation.Action{moderation.Allow, moderation.Reject, "review"} {
		t.Run(string(action), func(t *testing.T) {
			e, cmd := lostProjectionApproval(t)
			c := e.view(t, "p1").Case
			decision := appmoderation.DecisionCommand{
				CaseID:          c.ID,
				ExpectedVersion: c.Version,
				Operator:        moderation.Actor{ID: "o2"},
				Note:            "Повторная проверка",
			}
			var err error
			if action == moderation.Reject {
				_, err = e.service.Reject(context.Background(), decision)
			} else {
				_, err = e.service.Allow(context.Background(), decision)
			}
			if err != nil {
				t.Fatal(err)
			}
			if action == "review" {
				_, err := e.service.OpenCase(
					context.Background(),
					ids.OfferID{Tenant: "t1", Product: "p1"},
					"Повторная проверка",
					moderation.Actor{ID: "o2"},
				)
				if err != nil {
					t.Fatal(err)
				}
			}
			before, tasks := e.view(t, "p1"), e.store.OutboxRows()
			cmd.ExpectedVerdictVersion, cmd.Apply = before.Verdict.Version, true
			_, err = e.service.RecoverProjection(context.Background(), cmd)
			if err == nil || !reflect.DeepEqual(e.view(t, "p1"), before) ||
				!reflect.DeepEqual(e.store.OutboxRows(), tasks) {
				t.Fatal("последующее решение оператора обойдено", err)
			}
		})
	}
}

func TestRecoverProjectionRollsBackAllWrites(t *testing.T) {
	e, cmd := lostProjectionApproval(t)
	before, tasks := e.view(t, "p1"), e.store.OutboxRows()
	e.store.FailCommit = errors.New("сбой фиксации")
	cmd.Apply = true
	_, err := e.service.RecoverProjection(context.Background(), cmd)
	e.store.FailCommit = nil
	if err == nil || !reflect.DeepEqual(e.view(t, "p1"), before) || !reflect.DeepEqual(e.store.OutboxRows(), tasks) {
		t.Fatal("неуспешная транзакция частично сохранила перенос", err)
	}
}

func lostProjectionApproval(t *testing.T) (environment, appmoderation.RecoverProjectionCommand) {
	t.Helper()
	e := approvedProjectionEnvironment(t, true, "image-1")
	approved := e.view(t, "p1").Verdict
	previous := reviewedProjectionSnapshot(e, 1)
	e.clock.Advance(time.Hour)
	current := reviewedProjectionSnapshot(e, 2)
	legacy := appcatalog.NewProjector(e.store, e.clock, revisionOnlyEvaluator{service: e.service})
	if err := legacy.ApplyProduct(context.Background(), appcatalog.ProductEvent{
		EventID:  "revision-only-evaluation",
		Snapshot: current,
	}); err != nil {
		t.Fatal(err)
	}
	view := e.view(t, "p1")
	if view.Verdict.Status != moderation.Pending || view.Case == nil {
		t.Fatal("сценарий прежней rev-only оценки не воспроизведён", view)
	}
	return e, appmoderation.RecoverProjectionCommand{
		Previous:               previous,
		Current:                current,
		ExpectedRevision:       current.Revision,
		ExpectedVerdictVersion: view.Verdict.Version,
		ApprovedVerdictVersion: approved.Version,
	}
}

func (e revisionOnlyEvaluator) Evaluate(
	ctx context.Context,
	tx ports.Tx,
	product *catalog.Product,
	member *participant.Participant,
	_ *catalog.ProductState,
	now time.Time,
) error {
	return e.service.Evaluate(ctx, tx, product, member, nil, now)
}
