//go:build integration

package postgres_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	appreference "github.com/proshop-app/proshop-marketplace/internal/application/reference"
	"github.com/proshop-app/proshop-marketplace/internal/domain/reference"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/postgres"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/random"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

const (
	readReviewTask       = `SELECT payload FROM outbox WHERE kind = 'reference.changed' ORDER BY id DESC LIMIT 1`
	lockReviewDictionary = `SELECT code FROM reference.dictionaries WHERE code = 'b1' FOR UPDATE`
)

type reviewFaultUOW struct {
	base   ports.UnitOfWork
	calls  int
	cancel context.CancelFunc
}

type reviewFaultTx struct {
	ports.Tx
	reference reference.Repository
}

type reviewFaultRepository struct {
	reference.Repository
	owner *reviewFaultUOW
}

func (u *reviewFaultUOW) Do(ctx context.Context, fn func(context.Context, ports.Tx) error) error {
	return u.base.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		repository := &reviewFaultRepository{Repository: tx.Reference(), owner: u}
		return fn(ctx, reviewFaultTx{Tx: tx, reference: repository})
	})
}

func (tx reviewFaultTx) Reference() reference.Repository { return tx.reference }

func (r *reviewFaultRepository) Save(ctx context.Context, saved, deleted []*reference.Entry) error {
	if err := r.Repository.Save(ctx, saved, deleted); err != nil {
		return err
	}
	r.owner.calls++
	if r.owner.cancel != nil {
		r.owner.cancel()
	} else if r.owner.calls == 2 {
		return errors.New("тестовый отказ после записи второй пачки")
	}
	return nil
}

func seedReviewDatabase(t *testing.T, service *appreference.Service, count, aliasCount int) appreference.Snapshot {
	t.Helper()
	entries := make([]reference.DeliveryEntry, count)
	for i := range entries {
		entries[i] = reference.DeliveryEntry{
			Canonical: fmt.Sprintf("Значение %05d", i),
			Status:    reference.StatusCandidate,
			Metadata:  []byte(`{"label":"Подсказка","directions":["c1"]}`),
		}
		if aliasCount == 49 {
			entries[i].Metadata = []byte(`{"label":"` + strings.Repeat("x", 3980) + `"}`)
		}
		for alias := 0; alias < aliasCount; alias++ {
			entries[i].Aliases = append(entries[i].Aliases, reference.DeliveryAlias{
				Text: reviewAliasText(i, alias, aliasCount),
			})
		}
	}
	if _, err := service.Apply(context.Background(), reference.Delivery{
		Dictionary: referenceDict,
		Version:    1,
		Checksum:   "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		Entries:    entries,
	}); err != nil {
		t.Fatal(err)
	}
	snapshot, err := service.Snapshot(context.Background(), referenceDict, nil)
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func TestReferenceReviewAllCandidatesRealDatabaseAndCompleteDelta(t *testing.T) {
	service, pool, _ := referenceService(t)
	baseline := seedReviewDatabase(t, service, 2205, 1)
	excluded := baseline.Entries[0].ID
	command := appreference.ReviewCommand{
		Dictionary: referenceDict,
		IfMatch:    baseline.Revision,
		Status:     reference.StatusConfirmed,
		Selection:  reference.ReviewSelection{Mode: reference.ReviewAllCandidates, ExcludedIDs: []ids.UUID{excluded}},
		Meta:       referenceMeta,
	}
	started := time.Now()
	result, err := service.Review(context.Background(), command)
	if err != nil || result.Changed != 2204 {
		t.Fatal(result, err)
	}
	t.Logf("2204 кандидата обработаны за %s", time.Since(started))
	delta, err := service.Snapshot(context.Background(), referenceDict, &baseline.Revision)
	if err != nil || delta.Full || len(delta.Entries) != 2204 {
		t.Fatal(len(delta.Entries), delta.Full, err)
	}
	for _, entry := range delta.Entries {
		if entry.Status != reference.StatusConfirmed || entry.ID == excluded || len(entry.Aliases) != 2 {
			t.Fatal("неверная дельта", entry)
		}
	}
	var payload []byte
	if err := pool.QueryRow(context.Background(), readReviewTask).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	var event appreference.Event
	if err := json.Unmarshal(payload, &event); err != nil {
		t.Fatal(err)
	}
	if !event.Truncated || len(event.EntryIDs) != 1000 || len(event.AffectedKeys) != 1000 ||
		event.RevisionFrom != baseline.Revision || event.RevisionTo != result.Revision ||
		!reflect.DeepEqual(event.Kinds, []reference.EventKind{reference.EventStatusChanged}) {
		t.Fatal(event)
	}
	if _, err := service.Review(context.Background(), command); !errors.Is(err, reference.ErrDictionaryChanged) {
		t.Fatal("повтор без свежего выбора принят", err)
	}
	command.IfMatch = result.Revision
	command.Status = reference.StatusRejected
	command.Selection.ExcludedIDs = []ids.UUID{}
	last, err := service.Review(context.Background(), command)
	if err != nil || last.Changed != 1 {
		t.Fatal("пустое исключение потеряло последний кандидат", last, err)
	}
}

func TestReferenceReviewRealRollbackAfterSavedChunksAndCancellation(t *testing.T) {
	for _, cancelDuringSave := range []bool{false, true} {
		t.Run(fmt.Sprint(cancelDuringSave), func(t *testing.T) {
			service, pool, clock := referenceService(t)
			baseline := seedReviewDatabase(t, service, 1001, 0)
			var beforeChanges, beforeTasks int
			if err := pool.QueryRow(context.Background(), countReferenceChanges).Scan(&beforeChanges); err != nil {
				t.Fatal(err)
			}
			if err := pool.QueryRow(context.Background(), countReferenceTasks).Scan(&beforeTasks); err != nil {
				t.Fatal(err)
			}
			fault := &reviewFaultUOW{base: postgres.NewUnitOfWork(pool)}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if cancelDuringSave {
				fault.cancel = cancel
			}
			failing := appreference.NewService(fault, clock, random.IDs{}, nil, appreference.Config{})
			result, err := failing.Review(ctx, appreference.ReviewCommand{
				Dictionary: referenceDict,
				IfMatch:    baseline.Revision,
				Status:     reference.StatusConfirmed,
				Selection:  reference.ReviewSelection{Mode: reference.ReviewAllCandidates, ExcludedIDs: []ids.UUID{}},
				Meta:       referenceMeta,
			})
			if err == nil || result.Changed != 0 || fault.calls < 1 {
				t.Fatal(result, fault.calls, err)
			}
			after, err := service.Snapshot(context.Background(), referenceDict, nil)
			if err != nil || !reflect.DeepEqual(after, baseline) {
				t.Fatal("часть решения зафиксирована", err)
			}
			var afterChanges, afterTasks int
			if err := pool.QueryRow(context.Background(), countReferenceChanges).Scan(&afterChanges); err != nil {
				t.Fatal(err)
			}
			if err := pool.QueryRow(context.Background(), countReferenceTasks).Scan(&afterTasks); err != nil {
				t.Fatal(err)
			}
			if afterChanges != beforeChanges || afterTasks != beforeTasks {
				t.Fatal(afterChanges, beforeChanges, afterTasks, beforeTasks)
			}
		})
	}
}

func TestReferenceReviewTenThousandWithinRequestBudget(t *testing.T) {
	service, _, _ := referenceService(t)
	baseline := seedReviewDatabase(t, service, reference.MaxReviewEntries, 0)
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	started := time.Now()
	result, err := service.Review(ctx, appreference.ReviewCommand{
		Dictionary: referenceDict,
		IfMatch:    baseline.Revision,
		Status:     reference.StatusRejected,
		Selection:  reference.ReviewSelection{Mode: reference.ReviewAllCandidates, ExcludedIDs: []ids.UUID{}},
		Meta:       referenceMeta,
	})
	if err != nil || result.Changed != reference.MaxReviewEntries {
		t.Fatal(result, err)
	}
	t.Logf("10000 кандидатов обработаны за %s", time.Since(started))
}

func TestReferenceReviewDeadlineCancelsWaitingDictionaryLock(t *testing.T) {
	service, pool, _ := referenceService(t)
	baseline := seedReviewDatabase(t, service, 1, 0)
	lock, err := pool.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lock.Rollback(context.Background()) }()
	if _, err := lock.Exec(context.Background(), lockReviewDictionary); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	result, err := service.Review(ctx, appreference.ReviewCommand{
		Dictionary: referenceDict,
		IfMatch:    baseline.Revision,
		Status:     reference.StatusConfirmed,
		Selection:  reference.ReviewSelection{Mode: reference.ReviewAllCandidates, ExcludedIDs: []ids.UUID{}},
		Meta:       referenceMeta,
	})
	if err == nil || result.Changed != 0 || ctx.Err() != context.DeadlineExceeded {
		t.Fatal(result, err, ctx.Err())
	}
	_ = lock.Rollback(context.Background())
	after, err := service.Snapshot(context.Background(), referenceDict, nil)
	if err != nil || !reflect.DeepEqual(after, baseline) {
		t.Fatal(after, err)
	}
}

func reviewAliasText(entry, alias, count int) string {
	if count == 49 {
		return strings.Repeat("я", 105) + fmt.Sprintf(" %05d %02d", entry, alias)
	}
	return fmt.Sprintf("Вариант %05d %02d", entry, alias)
}

func TestReferenceReviewRichCandidatesKeepsAllAliasesAndMetadata(t *testing.T) {
	service, _, _ := referenceService(t)
	baseline := seedReviewDatabase(t, service, 1001, 49)
	revision := baseline.Revision
	baseline = appreference.Snapshot{}
	started := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	result, err := service.Review(ctx, appreference.ReviewCommand{
		Dictionary: referenceDict,
		IfMatch:    revision,
		Status:     reference.StatusConfirmed,
		Selection:  reference.ReviewSelection{Mode: reference.ReviewAllCandidates, ExcludedIDs: []ids.UUID{}},
		Meta:       referenceMeta,
	})
	if err != nil || result.Changed != 1001 {
		t.Fatal(result, err)
	}
	page, err := service.List(context.Background(), referenceDict, reference.ListFilter{Limit: 1})
	if err != nil || len(page.Entries) != 1 || len(page.Entries[0].Aliases) != 50 ||
		len(page.Entries[0].Metadata) < 3990 {
		t.Fatal("rich запись повреждена", page, err)
	}
	t.Logf("1001 кандидат с 50 вариантами и 4 КиБ metadata: %s", time.Since(started))
}
