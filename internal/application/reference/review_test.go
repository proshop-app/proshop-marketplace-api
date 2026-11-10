package reference

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"sync"
	"testing"

	"github.com/proshop-app/proshop-marketplace/internal/domain/reference"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

func seedReviewCandidates(t *testing.T, h *harness, count int) Snapshot {
	t.Helper()
	entries := make([]reference.DeliveryEntry, count)
	for i := range entries {
		entries[i] = reference.DeliveryEntry{
			Canonical: fmt.Sprintf("Значение %05d", i),
			Status:    reference.StatusCandidate,
			Metadata:  []byte(`{"label":"Подсказка","directions":["c1"]}`),
		}
	}
	if _, err := h.service.Apply(context.Background(), reference.Delivery{
		Dictionary: testDict,
		Version:    1,
		Checksum:   "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Entries:    entries,
	}); err != nil {
		t.Fatal(err)
	}
	snapshot, err := h.service.Snapshot(context.Background(), testDict, nil)
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func TestReviewAllExclusionsChunksJournalAndTruncatedDelta(t *testing.T) {
	h := newHarness(t)
	baseline := seedReviewCandidates(t, h, 1102)
	excluded := baseline.Entries[0].ID
	beforeChanges, beforeEvents := len(h.store.ReferenceChanges()), len(h.events(t))
	result, err := h.service.Review(context.Background(), ReviewCommand{
		Dictionary: testDict,
		IfMatch:    baseline.Revision,
		Status:     reference.StatusConfirmed,
		Selection:  reference.ReviewSelection{Mode: reference.ReviewAllCandidates, ExcludedIDs: []ids.UUID{excluded}},
		Meta:       operatorMeta,
	})
	if err != nil || result.Changed != 1101 || result.Revision != baseline.Revision+3 {
		t.Fatal(result, err)
	}
	changes := h.store.ReferenceChanges()[beforeChanges:]
	if len(changes) != 3 || len(changes[0].EntryIDs) != 500 || len(changes[2].EntryIDs) != 101 {
		t.Fatal("пачки журнала не ограничены", len(changes))
	}
	for _, change := range changes {
		if change.Meta != operatorMeta || change.Kind != reference.ChangeUpdated {
			t.Fatal("потерян автор решения", change)
		}
		for i, image := range change.Forward {
			if image.State.Status != reference.StatusConfirmed ||
				change.Inverse[i].State.Status != reference.StatusCandidate {
				t.Fatal("неверные прямой/обратный образы", change.Revision)
			}
			if !reflect.DeepEqual(image.State.Metadata, change.Inverse[i].State.Metadata) {
				t.Fatal("решение поменяло metadata")
			}
		}
	}
	events := h.events(t)[beforeEvents:]
	if len(events) != 1 || !events[0].Truncated || len(events[0].EntryIDs) != maxEventList ||
		len(events[0].AffectedKeys) != maxEventList || events[0].RevisionFrom != baseline.Revision ||
		events[0].RevisionTo != result.Revision ||
		!reflect.DeepEqual(events[0].Kinds, []reference.EventKind{reference.EventStatusChanged}) {
		t.Fatal("неверное агрегированное событие", events)
	}
	delta, err := h.service.Snapshot(context.Background(), testDict, &baseline.Revision)
	if err != nil || delta.Full || len(delta.Entries) != 1101 || len(delta.Removed) != 0 {
		t.Fatal("дельта потеряла обрезанные записи события", len(delta.Entries), delta.Full, err)
	}
	kept, _ := h.store.ReferenceEntry(excluded)
	if kept.Status != reference.StatusCandidate {
		t.Fatal("исключённый кандидат изменён", kept)
	}
}

func TestReviewOnlyCandidatesAndAllNonCandidatesRemainExact(t *testing.T) {
	h := newHarness(t)
	candidate := h.create(t, "Кандидат", reference.StatusCandidate)
	rejected := h.create(t, "Отклонённый", reference.StatusCandidate)
	rejectedStatus := reference.StatusRejected
	if _, err := h.service.Update(context.Background(), UpdateCommand{
		Dictionary: testDict,
		ID:         rejected.Entry.ID,
		IfMatch:    rejected.Entry.Revision,
		Status:     &rejectedStatus,
		Meta:       operatorMeta,
	}); err != nil {
		t.Fatal(err)
	}
	deprecated := h.create(t, "Выведенный", reference.StatusConfirmed)
	deprecatedStatus := reference.StatusDeprecated
	if _, err := h.service.Update(context.Background(), UpdateCommand{
		Dictionary: testDict,
		ID:         deprecated.Entry.ID,
		IfMatch:    deprecated.Entry.Revision,
		Status:     &deprecatedStatus,
		Meta:       operatorMeta,
	}); err != nil {
		t.Fatal(err)
	}
	confirmed := h.create(t, "Подтверждённый", reference.StatusConfirmed)
	merged := h.create(t, "Объединённый", reference.StatusConfirmed)
	if _, err := h.service.Merge(context.Background(), MergeCommand{
		Dictionary: testDict,
		ID:         merged.Entry.ID,
		Into:       confirmed.Entry.ID,
		IfMatch:    merged.Entry.Revision,
		Meta:       operatorMeta,
	}); err != nil {
		t.Fatal(err)
	}
	preserved := map[ids.UUID]reference.EntryState{}
	for _, entry := range []EntryResult{rejected, deprecated, confirmed, merged} {
		state, _ := h.store.ReferenceEntry(entry.Entry.ID)
		preserved[state.ID] = state
	}
	current, _ := h.service.Snapshot(context.Background(), testDict, nil)
	result, err := h.service.Review(context.Background(), ReviewCommand{
		Dictionary: testDict,
		IfMatch:    current.Revision,
		Status:     reference.StatusRejected,
		Selection:  reference.ReviewSelection{Mode: reference.ReviewAllCandidates, ExcludedIDs: []ids.UUID{}},
		Meta:       operatorMeta,
	})
	if err != nil || result.Changed != 1 {
		t.Fatal(result, err)
	}
	state, _ := h.store.ReferenceEntry(candidate.Entry.ID)
	if state.Status != reference.StatusRejected {
		t.Fatal(state)
	}
	for id, want := range preserved {
		got, _ := h.store.ReferenceEntry(id)
		if !reflect.DeepEqual(got, want) {
			t.Fatal("не кандидат изменён", got, want)
		}
	}
}

func TestReviewLaterChunkConflictAndCommitFailureRollbackEverything(t *testing.T) {
	for _, commitFailure := range []bool{false, true} {
		h := newHarness(t)
		baseline := seedReviewCandidates(t, h, 501)
		selected := make([]ids.UUID, 0, 502)
		for _, entry := range baseline.Entries {
			selected = append(selected, entry.ID)
		}
		if commitFailure {
			h.store.FailCommit = errors.New("сбой commit")
		} else {
			final := h.create(t, "Уже подтверждённый", reference.StatusConfirmed)
			selected = append(selected, final.Entry.ID)
			baseline, _ = h.service.Snapshot(context.Background(), testDict, nil)
		}
		beforeChanges, beforeEvents := len(h.store.ReferenceChanges()), len(h.events(t))
		result, err := h.service.Review(context.Background(), ReviewCommand{
			Dictionary: testDict,
			IfMatch:    baseline.Revision,
			Status:     reference.StatusConfirmed,
			Selection:  reference.ReviewSelection{Mode: reference.ReviewIDs, IDs: selected},
			Meta:       operatorMeta,
		})
		h.store.FailCommit = nil
		if err == nil || result.Changed != 0 || result.Revision != 0 {
			t.Fatal("частичный успех", result, err)
		}
		after, _ := h.service.Snapshot(context.Background(), testDict, nil)
		if !reflect.DeepEqual(after, baseline) || len(h.store.ReferenceChanges()) != beforeChanges ||
			len(h.events(t)) != beforeEvents {
			t.Fatal("часть решения пережила rollback", commitFailure)
		}
	}
}

func TestReviewCASAndSelectionConflictsDoNotSkip(t *testing.T) {
	h := newHarness(t)
	baseline := seedReviewCandidates(t, h, 2)
	command := ReviewCommand{
		Dictionary: testDict,
		IfMatch:    baseline.Revision,
		Status:     reference.StatusRejected,
		Selection:  reference.ReviewSelection{Mode: reference.ReviewIDs, IDs: []ids.UUID{baseline.Entries[0].ID}},
		Meta:       operatorMeta,
	}
	first, err := h.service.Review(context.Background(), command)
	if err != nil || first.Changed != 1 {
		t.Fatal(first, err)
	}
	if _, err := h.service.Review(context.Background(), command); !errors.Is(err, reference.ErrDictionaryChanged) {
		t.Fatal("stale решение повторено", err)
	}
	h.store.SeedReferenceDictionary(reference.DictionaryState{
		Code:       "c2",
		Kind:       reference.KindOpen,
		AliasKinds: reference.AliasKinds,
		Limits:     reference.DefaultLimits(),
	})
	foreign, err := h.service.Create(context.Background(), CreateCommand{
		Dictionary: "c2",
		Name:       "Чужой кандидат",
		Status:     reference.StatusCandidate,
		Meta:       operatorMeta,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, selection := range []reference.ReviewSelection{
		{Mode: reference.ReviewIDs, IDs: []ids.UUID{baseline.Entries[0].ID}},
		{Mode: reference.ReviewIDs, IDs: []ids.UUID{"00000000-0000-0000-0000-ffffffffffff"}},
		{Mode: reference.ReviewAllCandidates, ExcludedIDs: []ids.UUID{baseline.Entries[0].ID}},
		{Mode: reference.ReviewIDs, IDs: []ids.UUID{foreign.Entry.ID}},
		{Mode: reference.ReviewAllCandidates, ExcludedIDs: []ids.UUID{foreign.Entry.ID}},
	} {
		command.IfMatch, command.Selection = first.Revision, selection
		if _, err := h.service.Review(context.Background(), command); !errors.Is(err, reference.ErrSelectionChanged) {
			t.Fatal("выбор молча обрезан", selection, err)
		}
	}
}

func TestReviewEmptyAllAndConcurrentCAS(t *testing.T) {
	h := newHarness(t)
	empty, err := h.service.Review(context.Background(), ReviewCommand{
		Dictionary: testDict,
		Status:     reference.StatusConfirmed,
		Selection:  reference.ReviewSelection{Mode: reference.ReviewAllCandidates, ExcludedIDs: []ids.UUID{}},
		Meta:       operatorMeta,
	})
	if err != nil || empty.Changed != 0 || empty.Revision != 0 || len(h.store.OutboxRows()) != 0 {
		t.Fatal(empty, err)
	}
	baseline := seedReviewCandidates(t, h, 2)
	results := make(chan error, 2)
	var wait sync.WaitGroup
	for _, status := range []reference.Status{reference.StatusConfirmed, reference.StatusRejected} {
		wait.Add(1)
		go func(target reference.Status) {
			defer wait.Done()
			_, err := h.service.Review(context.Background(), ReviewCommand{
				Dictionary: testDict,
				IfMatch:    baseline.Revision,
				Status:     target,
				Selection:  reference.ReviewSelection{Mode: reference.ReviewAllCandidates, ExcludedIDs: []ids.UUID{}},
				Meta:       operatorMeta,
			})
			results <- err
		}(status)
	}
	wait.Wait()
	close(results)
	winners, conflicts := 0, 0
	for err := range results {
		switch {
		case err == nil:
			winners++
		case errors.Is(err, reference.ErrDictionaryChanged):
			conflicts++
		default:
			t.Fatal(err)
		}
	}
	if winners != 1 || conflicts != 1 {
		t.Fatal(winners, conflicts)
	}
}

func TestReviewCapsActualTargetsAndAllowsExplicitSubset(t *testing.T) {
	h := newHarness(t)
	baseline := seedReviewCandidates(t, h, reference.MaxReviewEntries+1)
	command := ReviewCommand{
		Dictionary: testDict,
		IfMatch:    baseline.Revision,
		Status:     reference.StatusConfirmed,
		Selection:  reference.ReviewSelection{Mode: reference.ReviewAllCandidates, ExcludedIDs: []ids.UUID{}},
		Meta:       operatorMeta,
	}
	if _, err := h.service.Review(context.Background(), command); !errors.Is(err, reference.ErrLimit) {
		t.Fatal("цели молча обрезаны", err)
	}
	command.Selection = reference.ReviewSelection{Mode: reference.ReviewIDs, IDs: []ids.UUID{baseline.Entries[0].ID}}
	result, err := h.service.Review(context.Background(), command)
	if err != nil || result.Changed != 1 {
		t.Fatal(result, err)
	}
	after, _ := h.service.Snapshot(context.Background(), testDict, nil)
	candidates := 0
	for _, entry := range after.Entries {
		if entry.Status == reference.StatusCandidate {
			candidates++
		}
	}
	if candidates != reference.MaxReviewEntries {
		t.Fatal(candidates)
	}
	if !slices.Equal(after.Removed, baseline.Removed) {
		t.Fatal(after.Removed)
	}
}
