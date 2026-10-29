package reference

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	"github.com/proshop-app/proshop-marketplace/internal/domain/reference"
)

const testDict = "b1"

var (
	testStart    = time.Date(2026, 10, 29, 10, 0, 0, 0, time.UTC)
	operatorMeta = reference.Meta{Actor: "t1", Source: reference.SourceOperator}
)

type harness struct {
	service   *Service
	store     *fake.Store
	clock     *fake.Clock
	publisher *fake.Publisher
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	h := &harness{store: fake.NewStore(), clock: fake.NewClock(testStart), publisher: &fake.Publisher{}}
	h.store.SeedReferenceDictionary(reference.DictionaryState{
		Code:       testDict,
		Kind:       reference.KindOpen,
		Identity:   true,
		AliasKinds: reference.AliasKinds,
		Limits:     reference.DefaultLimits(),
	})
	h.service = NewService(h.store, h.clock, &fake.IDs{}, h.publisher, Config{Topic: "catalog.reference.changed"})
	return h
}

func (h *harness) create(t *testing.T, name string, status reference.Status, aliases ...string) EntryResult {
	t.Helper()
	specs := make([]AliasSpec, 0, len(aliases))
	for _, alias := range aliases {
		specs = append(specs, AliasSpec{Text: alias})
	}
	result, err := h.service.Create(context.Background(), CreateCommand{
		Dictionary: testDict,
		Name:       name,
		Status:     status,
		Aliases:    specs,
		Meta:       operatorMeta,
	})
	if err != nil {
		t.Fatalf("Create(%s): %v", name, err)
	}
	return result
}

// events — тела событий из outbox по порядку постановки.
func (h *harness) events(t *testing.T) []Event {
	t.Helper()
	var out []Event
	for _, row := range h.store.OutboxRows() {
		if row.Kind != TaskChanged {
			continue
		}
		var event Event
		if err := json.Unmarshal(row.Payload, &event); err != nil {
			t.Fatal(err)
		}
		out = append(out, event)
	}
	return out
}
