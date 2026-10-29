package reference

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/reference"
)

func TestResolveExactCandidatesAndRefusals(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	confirmed := h.create(t, "Бренд", reference.StatusConfirmed, "Brend")
	h.create(t, "Брендовый", reference.StatusCandidate)
	rejected := h.create(t, "Отказ", reference.StatusCandidate)
	status := reference.StatusRejected
	if _, err := h.service.Update(ctx, UpdateCommand{
		Dictionary: testDict,
		ID:         rejected.Entry.ID,
		IfMatch:    rejected.Entry.Revision,
		Status:     &status,
		Meta:       operatorMeta,
	}); err != nil {
		t.Fatal(err)
	}

	exact, err := h.service.Resolve(ctx, testDict, "  BREND ")
	if err != nil || exact.Match == nil || exact.Match.EntryID != confirmed.Entry.ID ||
		exact.Match.Confidence != "exact" || len(exact.Candidates) != 0 || exact.Normalizer != "v1" {
		t.Fatalf("точное совпадение: %+v %v", exact, err)
	}
	fuzzy, err := h.service.Resolve(ctx, testDict, "бре")
	if err != nil || fuzzy.Match != nil || len(fuzzy.Candidates) != 2 {
		t.Fatalf("кандидаты по префиксу: %+v %v", fuzzy, err)
	}
	refused, err := h.service.Resolve(ctx, testDict, "отказ")
	if err != nil || refused.Match != nil || len(refused.Candidates) != 0 {
		t.Fatalf("отклонённая запись совпала: %+v %v", refused, err)
	}
	long := strings.Repeat("б", MaxResolveText+1)
	if _, err := h.service.Resolve(ctx, testDict, long); !errors.Is(err, reference.ErrInvalid) {
		t.Fatalf("длинный текст: %v", err)
	}
	if _, err := h.service.Resolve(ctx, testDict, " \u00a0 "); !errors.Is(err, reference.ErrInvalid) {
		t.Fatalf("пустой текст: %v", err)
	}
}

func TestSnapshotFullAndDelta(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.create(t, "Бренд", reference.StatusConfirmed)
	full, err := h.service.Snapshot(ctx, testDict, nil)
	if err != nil || !full.Full || full.Revision != 1 || len(full.Entries) != 1 {
		t.Fatalf("полный снимок: %+v %v", full, err)
	}
	second := h.create(t, "Бренд Два", reference.StatusConfirmed)
	since := full.Revision
	delta, err := h.service.Snapshot(ctx, testDict, &since)
	if err != nil || delta.Full || delta.Revision != 2 || len(delta.Entries) != 1 ||
		delta.Entries[0].ID != second.Entry.ID {
		t.Fatalf("дельта: %+v %v", delta, err)
	}
	head := delta.Revision
	empty, err := h.service.Snapshot(ctx, testDict, &head)
	if err != nil || empty.Full || len(empty.Entries) != 0 {
		t.Fatalf("пустая дельта: %+v %v", empty, err)
	}
	ahead := head + 10
	if reset, _ := h.service.Snapshot(ctx, testDict, &ahead); !reset.Full {
		t.Fatal("since впереди головы дал дельту")
	}
	h.store.AgeReferenceChanges(1, 31*24*time.Hour)
	old := int64(0)
	if aged, _ := h.service.Snapshot(ctx, testDict, &old); !aged.Full || len(aged.Entries) != 2 {
		t.Fatalf("since за горизонтом дал дельту: %+v", aged)
	}
	fresh := int64(1)
	if inside, _ := h.service.Snapshot(ctx, testDict, &fresh); inside.Full {
		t.Fatal("since на горизонте дал полный снимок")
	}
}

func TestHandleTaskPublishesByDictionaryKey(t *testing.T) {
	h := newHarness(t)
	h.create(t, "Бренд", reference.StatusConfirmed)
	row := h.store.OutboxRows()[0]
	if err := h.service.HandleTask(context.Background(), ports.Task{Kind: row.Kind, Payload: row.Payload}); err != nil {
		t.Fatal(err)
	}
	messages := h.publisher.Messages()
	if len(messages) != 1 || string(messages[0].Key) != testDict || messages[0].Topic != "catalog.reference.changed" {
		t.Fatalf("сообщения: %+v", messages)
	}
	h.publisher.Err = errors.New("брокер недоступен")
	if err := h.service.HandleTask(context.Background(), ports.Task{Payload: row.Payload}); err == nil {
		t.Fatal("сбой брокера проглочен: задача не уйдёт на повтор")
	}
}

func TestStatsCountsCandidateQueue(t *testing.T) {
	h := newHarness(t)
	h.create(t, "Бренд", reference.StatusCandidate)
	h.create(t, "Бренд Два", reference.StatusConfirmed)
	stats, err := h.service.Stats(context.Background())
	if err != nil || len(stats) != 1 || stats[0].Candidates != 1 || stats[0].Entries != 2 || stats[0].Revision != 2 {
		t.Fatalf("статистика: %+v %v", stats, err)
	}
}

func TestListOrdersCandidatesByMeetings(t *testing.T) {
	h := newHarness(t)
	h.create(t, "Бренд", reference.StatusCandidate)
	h.create(t, "Бренд Два", reference.StatusCandidate)
	h.create(t, "бренд два", reference.StatusCandidate)
	page, err := h.service.List(context.Background(), testDict, reference.ListFilter{Status: reference.StatusCandidate})
	if err != nil || page.Total != 2 || page.Entries[0].CanonicalKey != "бренд два" {
		t.Fatalf("очередь кандидатов: %+v %v", page, err)
	}
	if _, err := h.service.List(context.Background(), testDict, reference.ListFilter{Limit: maxPage + 1}); err == nil {
		t.Fatal("страница больше потолка")
	}
}
