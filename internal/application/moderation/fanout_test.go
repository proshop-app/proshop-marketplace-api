package moderation_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	appmoderation "github.com/proshop-app/proshop-marketplace/internal/application/moderation"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/moderation"
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

type fanoutProbe struct {
	ports.UnitOfWork
	reads  int
	writes int
}

type fanoutTx struct {
	ports.Tx
	probe *fanoutProbe
}

type fanoutParticipants struct {
	participant.Repository
	probe *fanoutProbe
}

type fanoutOutbox struct {
	ports.Outbox
	probe *fanoutProbe
}

func (p *fanoutProbe) Do(ctx context.Context, run func(context.Context, ports.Tx) error) error {
	return p.UnitOfWork.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		return run(ctx, fanoutTx{Tx: tx, probe: p})
	})
}

func (tx fanoutTx) Participants() participant.Repository {
	return fanoutParticipants{Repository: tx.Tx.Participants(), probe: tx.probe}
}

func (tx fanoutTx) Outbox() ports.Outbox {
	return fanoutOutbox{Outbox: tx.Tx.Outbox(), probe: tx.probe}
}

func (p fanoutParticipants) TenantIDsAfter(ctx context.Context, after ids.TenantID, limit int) ([]ids.TenantID, error) {
	p.probe.reads++
	return p.Repository.TenantIDsAfter(ctx, after, limit)
}

func (p fanoutParticipants) List(ctx context.Context, page participant.Page) ([]*participant.Participant, int, error) {
	p.probe.reads++
	return p.Repository.List(ctx, page)
}

func (o fanoutOutbox) Enqueue(ctx context.Context, task ports.NewTask) error {
	o.probe.writes++
	return o.Outbox.Enqueue(ctx, task)
}

func (o fanoutOutbox) EnqueueBatch(ctx context.Context, tasks []ports.NewTask) error {
	o.probe.writes++
	return o.Outbox.EnqueueBatch(ctx, tasks)
}

func TestStopPhraseQueuesKeysetFanout(t *testing.T) {
	ctx := context.Background()
	e := setup()
	probe := &fanoutProbe{UnitOfWork: e.store}
	service := appmoderation.NewService(probe, e.clock, &fake.IDs{}, appmoderation.Config{})
	for i := range 205 {
		key := ids.TenantID(fmt.Sprintf("t%03d", i))
		e.store.SeedParticipant(participant.State{TenantID: key})
	}
	if err := service.AddStopPhrase(ctx, "Товар", moderation.Actor{}); err != nil {
		t.Fatal(err)
	}
	if err := service.RemoveStopPhrase(ctx, "Товар"); err != nil {
		t.Fatal(err)
	}
	rows := e.store.OutboxRows()
	if len(rows) != 1 || rows[0].Kind != appmoderation.TaskReevaluateAll || probe.reads != 0 || probe.writes != 2 {
		t.Fatalf("две правки до взятия задачи: %d строк, %d чтений, %d записей", len(rows), probe.reads, probe.writes)
	}
	if err := service.RemoveStopPhrase(ctx, "Товар"); !errors.Is(err, moderation.ErrNotFound) || probe.writes != 2 {
		t.Fatalf("отсутствующая фраза: %v, записей %d", err, probe.writes)
	}
	root := rows[0]
	initial := ports.Task{ID: root.ID, Kind: root.Kind, Payload: root.Payload}
	if err := service.HandleReevaluateAll(ctx, initial); err != nil {
		t.Fatal(err)
	}
	var continuation fake.OutboxRow
	first := 0
	for _, row := range e.store.OutboxRows() {
		if row.Kind == appmoderation.TaskReevaluate {
			first++
		}
		if row.Kind == appmoderation.TaskReevaluateAll && row.ID != root.ID {
			continuation = row
		}
	}
	if first != 200 || continuation.ID == 0 || probe.reads != 1 || probe.writes != 3 {
		t.Fatalf("первая страница: %d магазинов, %d чтений, %d записей", first, probe.reads, probe.writes)
	}
	task := ports.Task{ID: continuation.ID, Kind: continuation.Kind, Payload: continuation.Payload}
	if err := service.HandleReevaluateAll(ctx, task); err != nil {
		t.Fatal(err)
	}
	members := map[ids.TenantID]int{}
	for _, row := range e.store.OutboxRows() {
		if row.Kind == appmoderation.TaskReevaluate {
			members[row.TenantID]++
		}
	}
	if len(members) != 206 || members["t1"] != 1 || members["t200"] != 1 || probe.reads != 2 || probe.writes != 4 {
		t.Fatalf("весь обход: %d магазинов, %d чтений, %d записей", len(members), probe.reads, probe.writes)
	}
}

func TestStopPhraseRollsBackTaskTogetherWithRules(t *testing.T) {
	ctx := context.Background()
	e := setup()
	e.store.FailCommit = errors.New("сбой фиксации")
	if err := e.service.AddStopPhrase(ctx, "Товар", moderation.Actor{}); err == nil {
		t.Fatal("ожидался откат")
	}
	e.store.FailCommit = nil
	phrases, err := e.service.ListStopPhrases(ctx)
	if err != nil || len(phrases) != 0 || len(e.store.OutboxRows()) != 0 {
		t.Fatalf("изменения откатанной транзакции: %v, %v, %v", phrases, e.store.OutboxRows(), err)
	}
}
