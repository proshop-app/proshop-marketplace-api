package reference

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/proshop-app/proshop-marketplace/internal/domain/reference"
)

func TestColorConfirmationValidatesFinalState(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.store.SeedReferenceDictionary(reference.DictionaryState{
		Code:       "color",
		Kind:       reference.KindClosed,
		AliasKinds: reference.AliasKinds,
		Limits:     reference.DefaultLimits(),
	})
	result, err := h.service.Create(ctx, CreateCommand{
		Dictionary: "color",
		Name:       "Хаки",
		Status:     reference.StatusCandidate,
		Meta:       operatorMeta,
	})
	if err != nil {
		t.Fatal(err)
	}
	confirmed := reference.StatusConfirmed
	command := UpdateCommand{
		Dictionary: "color",
		ID:         result.Entry.ID,
		IfMatch:    result.Entry.Revision,
		Status:     &confirmed,
		Meta:       operatorMeta,
	}
	if _, err := h.service.Update(ctx, command); err == nil {
		t.Fatal("подтверждён цвет без базового")
	}
	command.Metadata = json.RawMessage(`{"baseColors":["Зелёный"]}`)
	result, err = h.service.Update(ctx, command)
	if err != nil || result.Entry.Status != confirmed {
		t.Fatalf("единая правка статуса и метаданных отклонена: %v", err)
	}
	command.IfMatch = result.Entry.Revision
	command.Metadata = json.RawMessage(`{}`)
	if _, err := h.service.Update(ctx, command); err == nil {
		t.Fatal("удалена обязательная привязка")
	}
	for _, row := range h.store.OutboxRows() {
		if row.Kind != TaskChanged {
			t.Fatalf("правка реестра запускает постороннюю задачу: %s", row.Kind)
		}
	}
}

func TestColorJournalCannotRestoreForbiddenState(t *testing.T) {
	h := newHarness(t)
	h.store.SeedReferenceDictionary(reference.DictionaryState{
		Code:       "color",
		Kind:       reference.KindClosed,
		AliasKinds: reference.AliasKinds,
		Limits:     reference.DefaultLimits(),
	})
	_, err := h.service.mutate(context.Background(), "color", func(ctx context.Context, m *session) error {
		state := reference.EntryState{
			ID:            "e1",
			Dictionary:    "color",
			CanonicalName: "Хаки",
			CanonicalKey:  "хаки",
			Status:        reference.StatusConfirmed,
		}
		// Историческое удаление могло сохранить запрещённое теперь состояние.
		change := reference.Change{
			Dictionary: "color",
			EntryID:    state.ID,
			Revision:   1,
			Forward:    []reference.Image{{ID: state.ID}},
			Inverse:    []reference.Image{{ID: state.ID, State: &state}},
		}
		return h.service.revert(ctx, m, change, operatorMeta)
	})
	if err == nil {
		t.Fatal("откат восстановил запрещённое состояние")
	}
	page, err := h.service.List(context.Background(), "color", reference.ListFilter{})
	if err != nil || page.Total != 0 || len(h.store.OutboxRows()) != 0 {
		t.Fatal("отказ оставил частичное изменение", err)
	}
}
