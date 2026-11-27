package reference

import (
	"context"
	"testing"

	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	apptaxonomy "github.com/proshop-app/proshop-marketplace/internal/application/taxonomy"
	"github.com/proshop-app/proshop-marketplace/internal/domain/reference"
	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
)

func TestEmptyOpenImportPublicationLearningSnapshotAndQueue(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	input := importPackageFixture()
	input.Dictionaries = input.Dictionaries[:1]
	input.Dictionaries[0].Entries = []reference.DeliveryEntry{}
	first, err := h.service.Import(ctx, "op1", input)
	if err != nil || len(first) != 1 || !first[0].Created || !first[0].Changed ||
		first[0].Revision != 0 || first[0].CreatedEntries != 0 {
		t.Fatal(first, err)
	}
	if len(h.events(t)) != 0 || len(h.store.ReferenceChanges()) != 0 {
		t.Fatal("пустой импорт не изменяет записи")
	}
	full, err := h.service.Snapshot(ctx, "a1", nil, "")
	if err != nil || !full.Full || full.Revision != 0 || full.Kind != reference.KindOpen ||
		full.Entries == nil || len(full.Entries) != 0 || full.Removed == nil {
		t.Fatal(full, err)
	}
	summaries, err := h.service.DictionarySummaries(ctx)
	if err != nil || len(summaries) != 2 || summaries[0].Code != "a1" ||
		summaries[0].Entries != 0 || summaries[0].Candidates != 0 {
		t.Fatal(summaries, err)
	}
	definition := taxonomy.Definition{
		Version: 1,
		Attributes: []taxonomy.Attribute{
			{Code: "a1", Name: "Параметр товара", Type: "string", Dictionary: "a1"},
		},
		Schemas: map[string]taxonomy.Schema{
			"s1": {Attributes: []string{"a1"}, Optional: []string{"a1"}},
		},
		Categories: []taxonomy.Category{
			{ID: "c1", Slug: "c1", Name: "Товары", Schema: "s1"},
		},
	}
	h.store.SeedTaxonomy(definition)
	publication := apptaxonomy.NewService(h.store, h.clock, nil)
	if _, err := publication.LoadPartWithOptions(
		ctx,
		"attributes",
		definition,
		1,
		apptaxonomy.LoadOptions{},
	); err != nil {
		t.Fatal("ссылка на пустой open не опубликована", err)
	}
	search := &fake.ReferenceCandidates{
		Respond: func(query ports.AttributeValuesQuery) (ports.AttributeValuesPage, error) {
			if query.Attribute != "a1" {
				t.Fatal("потеряна привязка опубликованного атрибута", query)
			}
			return ports.AttributeValuesPage{
				Items: []ports.AttributeValue{{Value: "Первое значение", Documents: 3, Sellers: 2}},
			}, nil
		},
	}
	learned, err := h.service.CollectCandidates(ctx, "a1", search)
	if err != nil || learned.Created != 1 {
		t.Fatal(learned, err)
	}
	queue, err := h.service.List(ctx, "a1", reference.ListFilter{Status: reference.StatusCandidate})
	if err != nil || len(queue.Entries) != 1 || queue.Entries[0].Source != reference.SourceCatalog ||
		queue.Entries[0].CanonicalName != "Первое значение" {
		t.Fatal(queue, err)
	}
	events := h.events(t)
	if len(events) != 1 || events[0].Dictionary != "a1" || events[0].RevisionFrom != 0 ||
		events[0].RevisionTo != 1 || len(events[0].EntryIDs) != 1 {
		t.Fatal("нет события первого кандидата", events)
	}
	for _, row := range h.store.OutboxRows() {
		if row.Kind == TaskChanged {
			if err := h.service.HandleTask(ctx, ports.Task{Kind: row.Kind, Payload: row.Payload}); err != nil {
				t.Fatal(err)
			}
		}
	}
	messages := h.publisher.Messages()
	if len(messages) != 1 || messages[0].Topic != "catalog.reference.changed" || string(messages[0].Key) != "a1" {
		t.Fatal("потеряна публикация события словаря", messages)
	}
	since := int64(0)
	delta, err := h.service.Snapshot(ctx, "a1", &since, "")
	if err != nil || delta.Full || delta.Revision != 1 || len(delta.Entries) != 1 ||
		delta.Entries[0].Status != reference.StatusCandidate {
		t.Fatal(delta, err)
	}
	again, err := h.service.Import(ctx, "op1", input)
	if err != nil || again[0].Changed || again[0].Created || again[0].Revision != 1 || len(h.events(t)) != 1 {
		t.Fatal("повтор пустого пакета изменил обученный словарь", again, err)
	}
	queue, err = h.service.List(ctx, "a1", reference.ListFilter{Status: reference.StatusCandidate})
	if err != nil || len(queue.Entries) != 1 {
		t.Fatal("повтор удалил кандидата", queue, err)
	}
}
