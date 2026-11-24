package moderation_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	appcatalog "github.com/proshop-app/proshop-marketplace/internal/application/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	appmoderation "github.com/proshop-app/proshop-marketplace/internal/application/moderation"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/domain/moderation"
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
	"github.com/proshop-app/proshop-marketplace/pkg/money"
)

type projectionCountingUOW struct {
	source ports.UnitOfWork
	calls  map[string]int
}

type projectionCountingTx struct {
	ports.Tx
	calls map[string]int
}

type projectionCountingOutbox struct {
	ports.Outbox
	calls map[string]int
}

type projectionCountingModerator struct {
	ports.ProjectionModerator
	calls map[string]int
}

func (u *projectionCountingUOW) Do(ctx context.Context, run func(context.Context, ports.Tx) error) error {
	return u.source.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		return run(ctx, projectionCountingTx{Tx: tx, calls: u.calls})
	})
}

func (tx projectionCountingTx) Products() catalog.ProductRepository {
	tx.calls["products"]++
	return tx.Tx.Products()
}

func (tx projectionCountingTx) Inbox() ports.Inbox {
	tx.calls["inbox"]++
	return tx.Tx.Inbox()
}

func (tx projectionCountingTx) Taxonomy() taxonomy.Repository {
	tx.calls["taxonomy"]++
	return tx.Tx.Taxonomy()
}

func (tx projectionCountingTx) ModerationRules() moderation.RulesRepository {
	tx.calls["rules"]++
	return tx.Tx.ModerationRules()
}

func (tx projectionCountingTx) ModerationVerdicts() moderation.VerdictRepository {
	tx.calls["verdicts"]++
	return tx.Tx.ModerationVerdicts()
}

func (tx projectionCountingTx) ModerationCases() moderation.CaseRepository {
	tx.calls["cases"]++
	return tx.Tx.ModerationCases()
}

func (tx projectionCountingTx) Participants() participant.Repository {
	tx.calls["participants"]++
	return tx.Tx.Participants()
}

func (tx projectionCountingTx) Offers() ports.OfferStates {
	tx.calls["offers"]++
	return tx.Tx.Offers()
}

func (tx projectionCountingTx) ProjectionValidation() ports.ProjectionValidation {
	tx.calls["sources"]++
	return tx.Tx.ProjectionValidation()
}

func (tx projectionCountingTx) Outbox() ports.Outbox {
	tx.calls["outbox"]++
	return projectionCountingOutbox{Outbox: tx.Tx.Outbox(), calls: tx.calls}
}

func (r projectionCountingOutbox) EnqueueBatch(ctx context.Context, tasks []ports.NewTask) error {
	for _, task := range tasks {
		r.calls["enqueue:"+task.Kind]++
	}
	return r.Outbox.EnqueueBatch(ctx, tasks)
}

func (r projectionCountingOutbox) Enqueue(ctx context.Context, task ports.NewTask) error {
	r.calls["enqueue:"+task.Kind]++
	return r.Outbox.Enqueue(ctx, task)
}

func (m projectionCountingModerator) EvaluateProjection(
	ctx context.Context,
	tx ports.Tx,
	product *catalog.Product,
	previous *catalog.ProductState,
	now time.Time,
) error {
	m.calls["evaluate"]++
	return m.ProjectionModerator.EvaluateProjection(ctx, tx, product, previous, now)
}

func (m projectionCountingModerator) ReuseProjection(
	ctx context.Context,
	tx ports.Tx,
	product *catalog.Product,
	reuse *ports.ProjectionReuse,
	now time.Time,
) error {
	m.calls["reuse"]++
	return m.ProjectionModerator.ReuseProjection(ctx, tx, product, reuse, now)
}

func projectionReuseEnvironment(t *testing.T, pending bool) (environment, map[string]int, *appcatalog.Projector) {
	t.Helper()
	e := setup()
	e.store.SeedTaxonomy(reviewedProjectionTaxonomy(1))
	member, _ := e.store.Participant("t1")
	if !pending {
		member.Trust = participant.Trusted
		e.store.SeedParticipant(member)
	}
	calls := map[string]int{}
	uow := &projectionCountingUOW{source: e.store, calls: calls}
	service := appmoderation.NewService(uow, e.clock, &fake.IDs{}, appmoderation.Config{RequireImage: true})
	counted := projectionCountingModerator{ProjectionModerator: service, calls: calls}
	projector := appcatalog.NewProjector(uow, e.clock, counted)
	applyProjectionSnapshot(t, projector, reviewedProjectionSnapshot(e, 1))
	if calls["evaluate"] != 1 || calls["enqueue:moderation.offer.status"] != 1 ||
		calls["enqueue:catalog.offer.sync"] != 1 || calls["sources"] != 0 {
		t.Fatal("новый товар не прошёл обычные проверки", calls)
	}
	clear(calls)
	return e, calls, projector
}

func applyProjectionSnapshot(t *testing.T, projector *appcatalog.Projector, snapshot catalog.Snapshot) {
	t.Helper()
	if err := projector.ApplyProduct(context.Background(), appcatalog.ProductEvent{
		Snapshot: snapshot,
	}); err != nil {
		t.Fatal(err)
	}
}

// projectionQueryCount — хранилища, к которым обратился снимок: транзакция пачки берёт каждое один раз
// и читает его сразу для всех товаров пачки, поэтому счёт не растёт с числом чтений внутри снимка.
func projectionQueryCount(calls map[string]int) int {
	queries := 0
	for _, name := range []string{
		"products",
		"inbox",
		"taxonomy",
		"rules",
		"verdicts",
		"cases",
		"participants",
		"offers",
		"sources",
		"outbox",
	} {
		queries += calls[name]
	}
	return queries
}

func TestPriceProjectionReusesValidation(t *testing.T) {
	for _, pending := range []bool{false, true} {
		name := "allowed"
		if pending {
			name = "pending"
		}
		t.Run(name, func(t *testing.T) {
			e, calls, projector := projectionReuseEnvironment(t, pending)
			before := e.view(t, "p1")
			previous, _ := e.store.Product("t1", "p1")
			snapshot := reviewedProjectionSnapshot(e, 2)
			snapshot.BasePrice = money.FromKopecks(200)
			snapshot.Price = money.FromKopecks(180)
			snapshot.Available++
			applyProjectionSnapshot(t, projector, snapshot)
			if calls["taxonomy"] != 0 || calls["rules"] != 0 || calls["participants"] != 0 ||
				calls["evaluate"] != 0 || calls["reuse"] != 1 || calls["outbox"] != 1 ||
				calls["enqueue:catalog.offer.sync"] != 1 || calls["enqueue:moderation.offer.status"] != 0 {
				t.Fatal("цена вызвала повторные проверки или статус", calls)
			}
			wantQueries := 4
			if pending {
				wantQueries = 6
			}
			if got := projectionQueryCount(calls); got != wantQueries {
				t.Fatal("число хранилищ", got, wantQueries, calls)
			}
			after := e.view(t, "p1")
			product, _ := e.store.Product("t1", "p1")
			if !reflect.DeepEqual(previous.Card.Publication, product.Card.Publication) ||
				after.Verdict.Status != before.Verdict.Status || after.Verdict.Revision != 2 ||
				!reflect.DeepEqual(after.Verdict.Reasons, before.Verdict.Reasons) || product.Price.Kopecks() != 180 {
				t.Fatal("прежняя проверка или новая цена потеряны", product, after)
			}
			if pending && (after.Case == nil || after.Case.Revision != 2 ||
				after.Case.Card.BasePrice != 200 || after.Case.Card.Price != 180) {
				t.Fatal("открытое дело осталось на прежней ревизии/цене", after.Case)
			}
			t.Logf("хранилищ ApplyProduct: %d; вызовов Current таксономии/правил: 0", wantQueries)
			clear(calls)
			applyProjectionSnapshot(t, projector, reviewedProjectionSnapshot(e, 3))
			if calls["evaluate"] != 0 || calls["reuse"] != 1 {
				t.Fatal("следующее изменение цены не переиспользовало проверку", calls)
			}
		})
	}
}

func TestProjectionValidationInputsTriggerEvaluation(t *testing.T) {
	tests := []struct {
		name string
		edit func(*catalog.Snapshot)
	}{
		{name: "name", edit: func(p *catalog.Snapshot) { p.Card.Name = "Другой товар" }},
		{name: "description", edit: func(p *catalog.Snapshot) { p.Card.Description = "Другой текст" }},
		{name: "image", edit: func(p *catalog.Snapshot) { p.Card.Image = "image-2" }},
		{name: "category", edit: func(p *catalog.Snapshot) { p.Card.Marketplace.CategoryID = "c2" }},
		{
			name: "attribute",
			edit: func(p *catalog.Snapshot) {
				p.Card.Marketplace.Attributes["a1"] = json.RawMessage(`false`)
			},
		},
		{name: "published", edit: func(p *catalog.Snapshot) { p.Card.Marketplace.Published = false }},
		{name: "marketplace removed", edit: func(p *catalog.Snapshot) { p.Card.Marketplace = nil }},
		{name: "deleted", edit: func(p *catalog.Snapshot) { p.Deleted = true }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			e, calls, projector := projectionReuseEnvironment(t, false)
			snapshot := reviewedProjectionSnapshot(e, 2)
			test.edit(&snapshot)
			applyProjectionSnapshot(t, projector, snapshot)
			if calls["evaluate"] != 1 || calls["taxonomy"] == 0 || calls["reuse"] != 0 ||
				calls["enqueue:moderation.offer.status"] != 1 || calls["enqueue:catalog.offer.sync"] != 1 {
				t.Fatal("изменение карточки не проверено или задачи дублируются", calls)
			}
			if test.name == "name" {
				if got := projectionQueryCount(calls); got != 7 {
					t.Fatal("ожидалось 7 хранилищ карточки", got, calls)
				}
				t.Log("хранилищ ApplyProduct: 7")
			}
		})
	}
}

func TestPriceProjectionRefreshesStaleValidation(t *testing.T) {
	tests := []struct {
		name string
		edit func(environment)
	}{
		{name: "taxonomy version", edit: func(e environment) { e.store.SeedTaxonomy(reviewedProjectionTaxonomy(2)) }},
		{
			name: "rules revision",
			edit: func(e environment) {
				err := e.store.Do(context.Background(), func(ctx context.Context, tx ports.Tx) error {
					return tx.ModerationRules().AddPhrase(ctx, moderation.StopPhrase{Key: "phrase", Text: "phrase"})
				})
				if err != nil {
					panic(err)
				}
			},
		},
		{
			name: "participant trust",
			edit: func(e environment) {
				member, _ := e.store.Participant("t1")
				member.Trust = participant.Probation
				e.store.SeedParticipant(member)
			},
		},
		{
			name: "unchecked publication",
			edit: func(e environment) {
				product, _ := e.store.Product("t1", "p1")
				product.Card.Publication.Version = 0
				e.store.SeedProduct(product)
			},
		},
		{
			name: "unchecked verdict",
			edit: func(e environment) {
				e.store.SeedVerdict(moderation.VerdictState{TenantID: "t1", ProductID: "p1"})
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			e, calls, projector := projectionReuseEnvironment(t, false)
			test.edit(e)
			snapshot := reviewedProjectionSnapshot(e, 2)
			applyProjectionSnapshot(t, projector, snapshot)
			if calls["evaluate"] != 1 || calls["taxonomy"] == 0 || calls["reuse"] != 0 ||
				calls["enqueue:moderation.offer.status"] != 1 {
				t.Fatal("устаревшая оценка переиспользована", calls)
			}
			after := e.view(t, "p1")
			if test.name == "taxonomy version" && after.Verdict.TaxonomyVersion != 2 {
				t.Fatal("версия таксономии не обновлена", after)
			}
			if test.name == "rules revision" && after.Verdict.RulesRevision != 2 {
				t.Fatal("ревизия правил не обновлена", after)
			}
		})
	}
}

func TestProjectionReuseRollsBackAtomically(t *testing.T) {
	e, _, projector := projectionReuseEnvironment(t, true)
	before := e.view(t, "p1")
	productBefore, _ := e.store.Product("t1", "p1")
	tasksBefore := e.store.OutboxRows()
	failure := errors.New("ошибка COMMIT")
	e.store.FailCommit = failure
	snapshot := reviewedProjectionSnapshot(e, 2)
	snapshot.Price = money.FromKopecks(80)
	err := projector.ApplyProduct(context.Background(), appcatalog.ProductEvent{
		Snapshot: snapshot,
	})
	e.store.FailCommit = nil
	productAfter, _ := e.store.Product("t1", "p1")
	if !errors.Is(err, failure) || !reflect.DeepEqual(productBefore, productAfter) ||
		!reflect.DeepEqual(before, e.view(t, "p1")) || !reflect.DeepEqual(tasksBefore, e.store.OutboxRows()) ||
		e.store.Processed("rollback") {
		t.Fatal("откат оставил товар, вердикт, дело, inbox или задачу")
	}
}

func TestPriceProjectionRespectsImagePolicyChanges(t *testing.T) {
	for _, tightened := range []bool{false, true} {
		e, calls, _ := projectionReuseEnvironment(t, false)
		initial := appmoderation.NewService(e.store, e.clock, &fake.IDs{}, appmoderation.Config{
			RequireImage: !tightened,
		})
		snapshot := reviewedProjectionSnapshot(e, 2)
		snapshot.Card.Image = ""
		applyProjectionSnapshot(t, appcatalog.NewProjector(e.store, e.clock, initial), snapshot)
		uow := &projectionCountingUOW{source: e.store, calls: calls}
		updated := appmoderation.NewService(uow, e.clock, &fake.IDs{}, appmoderation.Config{RequireImage: tightened})
		moderator := projectionCountingModerator{ProjectionModerator: updated, calls: calls}
		clear(calls)
		snapshot.Revision++
		snapshot.Price = money.FromKopecks(200)
		applyProjectionSnapshot(t, appcatalog.NewProjector(uow, e.clock, moderator), snapshot)
		wantStatus := moderation.Allowed
		if tightened {
			wantStatus = moderation.Rejected
		}
		if calls["evaluate"] != 1 || calls["reuse"] != 0 || e.view(t, "p1").Verdict.Status != wantStatus {
			t.Fatal("смена политики изображения не учтена", tightened, calls)
		}
	}
}
