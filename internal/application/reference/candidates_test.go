package reference

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/reference"
	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
)

func candidateDefinition() taxonomy.Definition {
	return taxonomy.Definition{
		Version:    1,
		Attributes: []taxonomy.Attribute{{Code: "a1", Type: "string", Dictionary: testDict}},
		Categories: []taxonomy.Category{
			{ID: "c1", Children: []taxonomy.Category{{ID: "c1.x"}, {ID: "c1.y"}}},
			{ID: "c2"},
		},
		DictionaryBindings: []taxonomy.DictionaryBinding{
			{CategoryID: "c1", AttributeCode: "a1", Mode: "extend", Dictionaries: []string{"d1"}},
			{CategoryID: "c1.x", AttributeCode: "a1", Mode: "replace", Dictionaries: []string{testDict}},
		},
	}
}

func TestCandidatesBindingsKnownKeysAndMetadata(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.store.SeedReferenceDictionary(reference.DictionaryState{
		Code:       "d1",
		Kind:       reference.KindOpen,
		AliasKinds: reference.AliasKinds,
		Limits:     reference.DefaultLimits(),
	})
	h.store.SeedTaxonomy(candidateDefinition())
	known := h.create(t, "Занято", reference.StatusCandidate, "Другой ключ")
	rejected := reference.StatusRejected
	_, err := h.service.Update(ctx, UpdateCommand{
		Dictionary: testDict,
		ID:         known.Entry.ID,
		IfMatch:    known.Revision,
		Status:     &rejected,
		Meta:       operatorMeta,
	})
	if err != nil {
		t.Fatal(err)
	}
	before, err := h.service.Get(ctx, testDict, known.Entry.ID)
	if err != nil {
		t.Fatal(err)
	}
	search := &fake.ReferenceCandidates{
		Respond: func(query ports.AttributeValuesQuery) (ports.AttributeValuesPage, error) {
			if query.Attribute != "a1" || !reflect.DeepEqual(query.Categories, []string{"c1"}) ||
				!reflect.DeepEqual(query.ExcludeCategories, []string{"c1.x"}) {
				t.Fatal("поиск получил чужую область", query)
			}
			if query.After == "" {
				return ports.AttributeValuesPage{
					Items: []ports.AttributeValue{
						{Value: " ЗАНЯТО ", Documents: 20},
						{Value: "другой ключ", Documents: 15},
						{Value: "Новое", Documents: 12, Sellers: 3},
						{Value: "новое", Documents: 10},
						{Value: "Ещё", Documents: 6},
					},
					After: "next/+",
				}, nil
			}
			return ports.AttributeValuesPage{Items: []ports.AttributeValue{{Value: "Последнее", Documents: 4}}}, nil
		},
	}
	changes := len(h.store.ReferenceChanges())
	result, err := h.service.CollectCandidates(ctx, "d1", search)
	if err != nil || result.Created != 3 || len(search.Queries) != 2 || search.Queries[1].After != "next/+" {
		t.Fatal(result, search.Queries, err)
	}
	page, err := h.service.List(ctx, "d1", reference.ListFilter{})
	if err != nil || len(page.Entries) != 3 {
		t.Fatal(page, err)
	}
	for _, entry := range page.Entries {
		var metadata candidateMetadata
		if err := json.Unmarshal(entry.Metadata, &metadata); err != nil {
			t.Fatal(err)
		}
		catalog := metadata.Catalog
		if entry.Status != reference.StatusCandidate || entry.Source != reference.SourceCatalog ||
			catalog.Attribute != "a1" || !reflect.DeepEqual(catalog.Categories, []string{"c1"}) ||
			catalog.ObservedAt != testStart.Format("2006-01-02T15:04:05Z07:00") ||
			catalog.Documents != entry.Occurrences {
			t.Fatal(entry, catalog)
		}
		if entry.CanonicalName == "Новое" && (catalog.Sellers != 3 || catalog.Documents != 12) {
			t.Fatal("потеряна статистика каталога", catalog)
		}
	}
	after, err := h.service.Get(ctx, testDict, known.Entry.ID)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("владелец ключа изменён", after, err)
	}
	if len(h.store.ReferenceChanges()) != changes+2 ||
		len(h.store.ReferenceChanges()[changes].EntryIDs) != 2 {
		t.Fatal("страница должна сохраняться одним изменением")
	}
	result, err = h.service.CollectCandidates(ctx, "d1", search)
	if err != nil || result.Created != 0 || len(h.store.ReferenceChanges()) != changes+2 {
		t.Fatal("повтор меняет существующие записи", result, err)
	}
}

func TestCandidatesFailureDoesNotStopNextScope(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.store.SeedTaxonomy(candidateDefinition())
	unavailable := errors.New("поиск недоступен")
	search := &fake.ReferenceCandidates{
		Respond: func(query ports.AttributeValuesQuery) (ports.AttributeValuesPage, error) {
			if len(query.Categories) == 0 {
				if !reflect.DeepEqual(query.ExcludeCategories, []string{"c1"}) {
					t.Fatal("общая область захватила специальную ветку", query)
				}
				return ports.AttributeValuesPage{}, unavailable
			}
			if !reflect.DeepEqual(query.Categories, []string{"c1.x"}) {
				t.Fatal("общий словарь собирает специальную ветку", query)
			}
			return ports.AttributeValuesPage{Items: []ports.AttributeValue{{Value: "Значение", Documents: 5}}}, nil
		},
	}
	result, err := h.service.CollectCandidates(ctx, testDict, search)
	if result.Created != 1 || !errors.Is(err, unavailable) || len(search.Queries) != 2 {
		t.Fatal(result, search.Queries, err)
	}
}

func TestCandidatesSkipMissingTaxonomyUnboundAndClosed(t *testing.T) {
	h := newHarness(t)
	search := &fake.ReferenceCandidates{}
	ctx := context.Background()
	result, err := h.service.CollectCandidates(ctx, testDict, search)
	if err != nil || result.Created != 0 || len(search.Calls) != 0 {
		t.Fatal("пустая таксономия", result, err)
	}
	h.store.SeedTaxonomy(taxonomy.Definition{
		Version:    1,
		Attributes: []taxonomy.Attribute{{Code: "a1", Type: "string"}},
	})
	result, err = h.service.CollectCandidates(ctx, testDict, search)
	if err != nil || result.Created != 0 || len(search.Calls) != 0 {
		t.Fatal("нет привязки", result, err)
	}
	h.store.SeedTaxonomy(candidateDefinition())
	h.store.SeedReferenceDictionary(reference.DictionaryState{Code: testDict, Kind: reference.KindClosed})
	result, err = h.service.CollectCandidates(ctx, testDict, search)
	if err != nil || result.Created != 0 || len(search.Calls) != 0 {
		t.Fatal("закрытый словарь", result, err)
	}
}

func TestCandidatesPageRollbackAndRepeatedCursor(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.store.SeedTaxonomy(taxonomy.Definition{
		Version:    1,
		Attributes: []taxonomy.Attribute{{Code: "a1", Type: "string", Dictionary: testDict}},
	})
	search := &fake.ReferenceCandidates{
		Pages: map[string]map[string]ports.AttributeValuesPage{
			"a1": {"": {Items: []ports.AttributeValue{{Value: "Новое"}, {Value: ""}}}},
		},
	}
	result, err := h.service.CollectCandidates(ctx, testDict, search)
	if err == nil || result.Created != 0 || len(h.store.ReferenceChanges()) != 0 || len(h.events(t)) != 0 {
		t.Fatal("ошибка страницы не откатила пакет", result, err)
	}
	search.Pages["a1"] = map[string]ports.AttributeValuesPage{
		"":       {After: "repeat"},
		"repeat": {After: "repeat"},
	}
	result, err = h.service.CollectCandidates(ctx, testDict, search)
	if !errors.Is(err, reference.ErrInvalid) || result.Created != 0 {
		t.Fatal("повтор курсора не обнаружен", result, err)
	}
}

func TestCandidatesKnownKeyInEveryStatus(t *testing.T) {
	for _, status := range []reference.Status{
		reference.StatusCandidate,
		reference.StatusConfirmed,
		reference.StatusRejected,
		reference.StatusDeprecated,
	} {
		t.Run(string(status), func(t *testing.T) {
			h := newHarness(t)
			ctx := context.Background()
			h.store.SeedTaxonomy(candidateDefinition())
			h.store.SeedReferenceDictionary(reference.DictionaryState{
				Code:       "d1",
				Kind:       reference.KindOpen,
				AliasKinds: reference.AliasKinds,
				Limits:     reference.DefaultLimits(),
			})
			initial := reference.StatusCandidate
			if status == reference.StatusConfirmed || status == reference.StatusDeprecated {
				initial = reference.StatusConfirmed
			}
			known := h.create(t, "Значение", initial)
			if status != initial {
				if _, err := h.service.Update(ctx, UpdateCommand{
					Dictionary: testDict,
					ID:         known.Entry.ID,
					IfMatch:    known.Revision,
					Status:     &status,
					Meta:       operatorMeta,
				}); err != nil {
					t.Fatal(err)
				}
			}
			search := &fake.ReferenceCandidates{
				Pages: map[string]map[string]ports.AttributeValuesPage{
					"a1": {"": {Items: []ports.AttributeValue{{Value: "ЗНАЧЕНИЕ", Documents: 7}}}},
				},
			}
			result, err := h.service.CollectCandidates(ctx, "d1", search)
			if err != nil || result.Created != 0 {
				t.Fatal("занятый ключ стал кандидатом", result, err)
			}
		})
	}
}

func TestCandidatesClosedLastDictionaryHasNoFallback(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	d := candidateDefinition()
	d.DictionaryBindings = d.DictionaryBindings[:1]
	d.DictionaryBindings[0].CategoryID = ""
	h.store.SeedTaxonomy(d)
	h.store.SeedReferenceDictionary(reference.DictionaryState{Code: "d1", Kind: reference.KindClosed})
	search := &fake.ReferenceCandidates{}
	for _, code := range []string{testDict, "d1"} {
		result, err := h.service.CollectCandidates(ctx, code, search)
		if err != nil || result.Created != 0 || len(search.Calls) != 0 {
			t.Fatal("закрытый получатель подменён открытым", result, err)
		}
	}
}
