package moderation_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	appmoderation "github.com/proshop-app/proshop-marketplace/internal/application/moderation"
	"github.com/proshop-app/proshop-marketplace/internal/application/moderationstatus"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/moderation"
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

type statusReads struct {
	batch, single, latest, history, reasons, cases, members, memberHistory int
}

type statusCountingStore struct {
	ports.UnitOfWork
	reads *statusReads
}

type statusCountingTx struct {
	ports.Tx
	reads *statusReads
}

type statusCountingOffers struct {
	ports.OfferStates
	reads *statusReads
}

type statusCountingDecisions struct {
	moderation.DecisionRepository
	reads *statusReads
}

type statusCountingCases struct {
	moderation.CaseRepository
	reads *statusReads
}

func (s statusCountingStore) Do(ctx context.Context, fn func(context.Context, ports.Tx) error) error {
	return s.UnitOfWork.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		return fn(ctx, statusCountingTx{Tx: tx, reads: s.reads})
	})
}

func (tx statusCountingTx) Offers() ports.OfferStates {
	return statusCountingOffers{OfferStates: tx.Tx.Offers(), reads: tx.reads}
}

func (tx statusCountingTx) ModerationDecisions() moderation.DecisionRepository {
	return statusCountingDecisions{DecisionRepository: tx.Tx.ModerationDecisions(), reads: tx.reads}
}

func (tx statusCountingTx) ModerationCases() moderation.CaseRepository {
	return statusCountingCases{CaseRepository: tx.Tx.ModerationCases(), reads: tx.reads}
}

func (tx statusCountingTx) Participants() participant.Repository {
	tx.reads.members++
	return tx.Tx.Participants()
}

func (tx statusCountingTx) ParticipantDecisions() participant.DecisionRepository {
	tx.reads.memberHistory++
	return tx.Tx.ParticipantDecisions()
}

func (r statusCountingOffers) Batch(ctx context.Context, keys []ids.OfferID) ([]ports.OfferState, error) {
	r.reads.batch++
	return r.OfferStates.Batch(ctx, keys)
}

func (r statusCountingOffers) Offer(ctx context.Context, t ids.TenantID, p ids.ProductID) (ports.OfferState, error) {
	r.reads.single++
	return r.OfferStates.Offer(ctx, t, p)
}

func (r statusCountingDecisions) Latest(
	ctx context.Context,
	tenant ids.TenantID,
	products []ids.ProductID,
) (map[ids.ProductID]moderation.Decision, error) {
	r.reads.latest++
	return r.DecisionRepository.Latest(ctx, tenant, products)
}

func (r statusCountingDecisions) List(
	ctx context.Context,
	tenant ids.TenantID,
	product ids.ProductID,
) ([]moderation.Decision, error) {
	r.reads.history++
	return r.DecisionRepository.List(ctx, tenant, product)
}

func (r statusCountingCases) Get(ctx context.Context, id ids.UUID) (*moderation.Case, error) {
	r.reads.cases++
	return r.CaseRepository.Get(ctx, id)
}

func (r statusCountingCases) Reasons(ctx context.Context, cases []ids.UUID) (map[ids.UUID][]moderation.Reason, error) {
	r.reads.reasons++
	return r.CaseRepository.Reasons(ctx, cases)
}

func TestStatusBatchReadCount(t *testing.T) {
	for _, action := range []moderation.Action{moderation.Allow, moderation.Reject, "pending", "deleted"} {
		for _, count := range []int{1, 20, moderationstatus.MaxProductIDs} {
			t.Run(fmt.Sprintf("%s/%d", action, count), func(t *testing.T) {
				e := setup()
				products := seedStatusBatch(t, e, action, count)
				reads := &statusReads{}
				service := moderationstatus.NewService(
					statusCountingStore{UnitOfWork: e.store, reads: reads},
					e.clock,
					&fake.Publisher{},
					"offer",
					"participant",
				)
				snapshot, err := service.Get(context.Background(), "t1", products)
				want := statusReads{batch: 1}
				wantItems := count
				if action == moderation.Allow || action == moderation.Reject {
					want.latest = 1
				}
				if action == moderation.Reject {
					want.reasons = 1
				}
				if action == "deleted" {
					wantItems = 0
				}
				if err != nil || *reads != want || len(snapshot.Items) != wantItems || snapshot.Participant != nil {
					t.Fatalf("обращения %+v, ожидалось %+v; снимок %+v; ошибка %v", *reads, want, snapshot, err)
				}
				assertStatusDecisions(t, snapshot.Items, action)
			})
		}
	}
}

func seedStatusBatch(t *testing.T, e environment, action moderation.Action, count int) []ids.ProductID {
	t.Helper()
	products := make([]ids.ProductID, 0, count)
	for i := range count {
		product := ids.ProductID(fmt.Sprintf("p%d", i))
		e.project(t, product, 1)
		products = append(products, product)
	}
	for _, product := range products {
		if action == "deleted" {
			state, _ := e.store.Product("t1", product)
			state.Deleted = true
			e.store.SeedProduct(state)
		}
		if action != moderation.Allow && action != moderation.Reject {
			continue
		}
		c := e.view(t, product).Case
		decide := e.service.Allow
		if action == moderation.Reject {
			decide = e.service.Reject
		}
		if _, err := decide(context.Background(), appmoderation.DecisionCommand{
			CaseID:          c.ID,
			ExpectedVersion: c.Version,
			Note:            "Проверено",
		}); err != nil {
			t.Fatal(err)
		}
	}
	return products
}

func assertStatusDecisions(t *testing.T, items []*moderationstatus.OfferEvent, action moderation.Action) {
	t.Helper()
	for _, item := range items {
		if action == "pending" {
			if item.Decision != nil || item.Status != "pending" {
				t.Fatal(item)
			}
			continue
		}
		if item.Decision == nil || item.Decision.Action != action || item.Decision.Note != "Проверено" {
			t.Fatal(item)
		}
	}
}

func TestStatusParticipantReadsNoOffers(t *testing.T) {
	e := setup()
	reads := &statusReads{}
	service := moderationstatus.NewService(
		statusCountingStore{UnitOfWork: e.store, reads: reads},
		e.clock,
		&fake.Publisher{},
		"offer",
		"participant",
	)
	snapshot, err := service.Get(context.Background(), "t1", nil)
	if err != nil || snapshot.Participant == nil || snapshot.Items == nil || len(snapshot.Items) != 0 ||
		*reads != (statusReads{members: 1, memberHistory: 1}) {
		t.Fatal(snapshot, *reads, err)
	}
}

func TestStatusOutboxUsesSingleItemBatch(t *testing.T) {
	e := setup()
	seedStatusBatch(t, e, moderation.Reject, 1)
	reads := &statusReads{}
	publisher := &fake.Publisher{}
	service := moderationstatus.NewService(
		statusCountingStore{UnitOfWork: e.store, reads: reads},
		e.clock,
		publisher,
		"offer",
		"participant",
	)
	task := moderationstatus.OfferTask("t1", "p0", e.clock.Now())
	err := service.HandleTask(context.Background(), ports.Task{
		Kind:     task.Kind,
		TenantID: task.TenantID,
		Payload:  task.Payload,
	})
	if err != nil || *reads != (statusReads{batch: 1, latest: 1, reasons: 1}) || len(publisher.Messages()) != 1 {
		t.Fatal(*reads, publisher.Messages(), err)
	}
}

func TestStatusBatchSkipsMismatchedDecisions(t *testing.T) {
	e := setup()
	seedStatusBatch(t, e, moderation.Reject, 1)
	if err := e.store.Do(context.Background(), func(ctx context.Context, tx ports.Tx) error {
		history, err := tx.ModerationDecisions().List(ctx, "t1", "p0")
		if err != nil {
			return err
		}
		d := history[0]
		d.ID = "newer"
		d.Note = "Другое решение"
		return tx.ModerationDecisions().Add(ctx, d)
	}); err != nil {
		t.Fatal(err)
	}
	reads := &statusReads{}
	service := moderationstatus.NewService(
		statusCountingStore{UnitOfWork: e.store, reads: reads},
		e.clock,
		&fake.Publisher{},
		"offer",
		"participant",
	)
	snapshot, err := service.Get(context.Background(), "t1", []ids.ProductID{"p0"})
	if err != nil || len(snapshot.Items) != 1 || snapshot.Items[0].Decision != nil ||
		*reads != (statusReads{batch: 1, latest: 1}) {
		t.Fatal(snapshot, *reads, err)
	}
}
