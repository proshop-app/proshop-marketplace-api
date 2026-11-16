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
