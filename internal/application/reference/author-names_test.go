package reference

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/reference"
	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
)

const authorDictionary = "book_author"

func TestAuthorCreateAndRenamePreserveForeignAliases(t *testing.T) {
	h := newAuthorHarness(t)
	ctx := context.Background()
	created, err := h.service.Create(ctx, CreateCommand{
		Dictionary: authorDictionary,
		Name:       "Автор",
		Status:     reference.StatusCandidate,
		Aliases: []AliasSpec{
			{
				Text: "Foreign Author",
				Kind: reference.AliasLocale,
			},
		},
		Meta: operatorMeta,
	})

	if err != nil {
		t.Fatal(err)
	}

	before := len(h.events(t))
	foreign := "Foreign Author"

	if _, err := h.service.Create(ctx, CreateCommand{
		Dictionary: authorDictionary,
		Name:       foreign,
		Status:     reference.StatusCandidate,
		Meta:       operatorMeta,
	}); !errors.Is(err, reference.ErrInvalid) {
		t.Fatal("иноязычный canonical прошёл через занятый alias", err)
	}

	if _, err := h.service.Update(ctx, UpdateCommand{
		Dictionary: authorDictionary,
		ID:         created.Entry.ID,
		IfMatch:    created.Entry.Revision,
		Name:       &foreign,
		Meta:       operatorMeta,
	}); !errors.Is(err, reference.ErrInvalid) {
		t.Fatal("иноязычное переименование принято", err)
	}

	stored, err := h.service.Get(ctx, authorDictionary, created.Entry.ID)

	if err != nil || !reflect.DeepEqual(stored.Entry, created.Entry) || len(h.events(t)) != before {
		t.Fatal("отказ изменил запись или события", stored, err)
	}

	russian := "Новый Автор"
	renamed, err := h.service.Update(ctx, UpdateCommand{
		Dictionary: authorDictionary,
		ID:         created.Entry.ID,
		IfMatch:    created.Entry.Revision,
		Name:       &russian,
		Meta:       operatorMeta,
	})

	if err != nil || renamed.Entry.CanonicalName != russian || len(renamed.Entry.Aliases) != 3 {
		t.Fatal(renamed, err)
	}

	resolved, err := h.service.Resolve(ctx, authorDictionary, foreign)

	if err != nil || resolved.Match == nil || resolved.Match.Canonical != russian {
		t.Fatal("иноязычный alias не разрешается в русское имя", resolved, err)
	}

	h.create(t, foreign, reference.StatusCandidate)
}

func TestAuthorImportRejectsForeignCanonicalAtomically(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	input := importPackageFixture()
	input.Dictionaries[0].Entries[0].Canonical = "Foreign Brand"
	input.Dictionaries[1].Code = authorDictionary
	input.Dictionaries[1].Kind = reference.KindOpen
	input.Dictionaries[1].Entries[0].Canonical = "Foreign Author"

	if result, err := h.service.Import(ctx, "", input); result != nil || !errors.Is(err, reference.ErrInvalidDelivery) {
		t.Fatal("иноязычный canonical импортирован", result, err)
	}

	dictionaries, err := h.service.Dictionaries(ctx)

	if err != nil || len(dictionaries) != 1 || len(h.events(t)) != 0 {
		t.Fatal("неверный импорт частично применён", dictionaries, err)
	}

	input.Dictionaries[1].Entries[0].Canonical = "Автор"
	input.Dictionaries[1].Entries[0].Aliases = []reference.DeliveryAlias{
		{
			Text:   "Foreign Author",
			Kind:   reference.AliasLocale,
			Locale: "en",
		},
	}
	result, err := h.service.Import(ctx, "", input)

	if err != nil || len(result) != 2 {
		t.Fatal("допустимый импорт отклонён", result, err)
	}

	page, err := h.service.List(ctx, authorDictionary, reference.ListFilter{})

	if err != nil || len(page.Entries) != 1 || page.Entries[0].CanonicalName != "Автор" ||
		len(page.Entries[0].Aliases) != 2 {
		t.Fatal(page, err)
	}
}

func TestAuthorCandidateImportAddsAliasWithoutChangingConfirmedEntry(t *testing.T) {
	h := newAuthorHarness(t)
	ctx := context.Background()
	created, err := h.service.Create(ctx, CreateCommand{
		Dictionary: authorDictionary,
		Name:       "Автор",
		Status:     reference.StatusConfirmed,
		Metadata:   []byte(`{"operator":"keep"}`),
		Meta:       operatorMeta,
	})

	if err != nil {
		t.Fatal(err)
	}

	input := importPackageFixture()
	input.Dictionaries = []reference.ImportDictionary{
		{
			Code:    authorDictionary,
			Kind:    reference.KindOpen,
			Release: 2,
			Entries: []reference.DeliveryEntry{
				{
					Canonical: "Автор",
					Status:    reference.StatusCandidate,
					Aliases: []reference.DeliveryAlias{
						{
							Text:   "Foreign Author",
							Kind:   reference.AliasLocale,
							Locale: "en",
						},
					},
				},
			},
		},
	}
	imported, err := h.service.Import(ctx, "", input)

	if err != nil || len(imported) != 1 || !imported[0].Changed || imported[0].Created ||
		imported[0].CreatedEntries != 0 || imported[0].UpdatedEntries != 1 {
		t.Fatal(imported, err)
	}

	stored, err := h.service.Get(ctx, authorDictionary, created.Entry.ID)

	if err != nil || stored.Entry.ID != created.Entry.ID || stored.Entry.Status != reference.StatusConfirmed ||
		stored.Entry.Source != reference.SourceOperator ||
		!reflect.DeepEqual(stored.Entry.Metadata, created.Entry.Metadata) {
		t.Fatal("поставка изменила подтверждение или происхождение", stored, err)
	}

	hasOriginalAlias := false
	var addedAlias reference.AliasState

	for _, alias := range stored.Entry.Aliases {
		if alias.ID == created.Entry.Aliases[0].ID {
			hasOriginalAlias = reflect.DeepEqual(alias, created.Entry.Aliases[0])
		} else {
			addedAlias = alias
		}
	}

	if len(stored.Entry.Aliases) != len(created.Entry.Aliases)+1 || !hasOriginalAlias {
		t.Fatal("поставка изменила прежние aliases", stored.Entry.Aliases)
	}

	if addedAlias.Text != "Foreign Author" || addedAlias.Kind != reference.AliasLocale || addedAlias.Locale != "en" {
		t.Fatal("иноязычный alias не добавлен", addedAlias)
	}

	expected := created.Entry
	expected.Aliases = stored.Entry.Aliases

	if !reference.SameContent(&expected, &stored.Entry) || stored.Entry.Occurrences != created.Entry.Occurrences {
		t.Fatal("поставка изменила содержимое помимо aliases", stored.Entry)
	}

	resolved, err := h.service.Resolve(ctx, authorDictionary, "Foreign Author")

	if err != nil || resolved.Match == nil || resolved.Match.EntryID != created.Entry.ID ||
		resolved.Match.Canonical != "Автор" || resolved.Match.Status != reference.StatusConfirmed {
		t.Fatal("иноязычный alias не разрешается в прежнего русского автора", resolved, err)
	}

	events := h.events(t)
	changes := len(h.store.ReferenceChanges())
	repeated, err := h.service.Import(ctx, "", input)

	if err != nil || len(repeated) != 1 || repeated[0].Changed || repeated[0].Created ||
		repeated[0].CreatedEntries != 0 || repeated[0].UpdatedEntries != 0 ||
		repeated[0].Revision != imported[0].Revision || !reflect.DeepEqual(events, h.events(t)) ||
		len(h.store.ReferenceChanges()) != changes {
		t.Fatal("повтор поставки изменил реестр или события", repeated, err)
	}

	afterRepeat, err := h.service.Get(ctx, authorDictionary, created.Entry.ID)

	if err != nil || !reflect.DeepEqual(afterRepeat.Entry, stored.Entry) {
		t.Fatal("повтор поставки изменил запись", afterRepeat, err)
	}
}

func TestAuthorCollectorSkipsForeignObservationsAndContinuesPages(t *testing.T) {
	h := newAuthorHarness(t)
	ctx := context.Background()
	h.store.SeedTaxonomy(taxonomy.Definition{
		Version: 1,
		Attributes: []taxonomy.Attribute{
			{Code: authorDictionary, Type: "string", Dictionary: authorDictionary},
			{Code: testDict, Type: "string", Dictionary: testDict},
		},
	})
	search := &fake.ReferenceCandidates{
		Pages: map[string]map[string]ports.AttributeValuesPage{
			authorDictionary: {
				"": {
					Items: []ports.AttributeValue{
						{
							Value:     "Foreign Author",
							Documents: 20,
						},
						{
							Value:     "123 —",
							Documents: 10,
						},
						{
							Value:     "Автор A",
							Documents: 5,
						},
					},
					After: "next",
				},
				"next": {
					Items: []ports.AttributeValue{
						{
							Value:     "Автор",
							Documents: 3,
						},
						{
							Value:     "Другой Автор",
							Documents: 2,
						},
					},
				},
			},
			testDict: {
				"": {
					Items: []ports.AttributeValue{
						{
							Value:     "Foreign Brand",
							Documents: 4,
						},
					},
				},
			},
		},
	}
	created, err := h.service.CollectCandidates(ctx, authorDictionary, search)

	if err != nil || created.Created != 2 {
		t.Fatal("наблюдения остановили сбор", created, err)
	}

	page, err := h.service.List(ctx, authorDictionary, reference.ListFilter{})

	if err != nil || len(page.Entries) != 2 || page.Entries[0].CanonicalName != "Автор" ||
		page.Entries[0].Occurrences != 3 || page.Entries[1].CanonicalName != "Другой Автор" {
		t.Fatal(page, err)
	}

	created, err = h.service.CollectCandidates(ctx, authorDictionary, search)

	if err != nil || created.Created != 0 || len(search.Calls) != 4 {
		t.Fatal("повтор сбора изменил число кандидатов или пагинацию", created, search.Calls, err)
	}

	created, err = h.service.CollectCandidates(ctx, testDict, search)

	if err != nil || created.Created != 1 {
		t.Fatal("наблюдение другого словаря потеряно", created, err)
	}
}

func TestAuthorJournalRestoreCannotBypassCanonicalPolicy(t *testing.T) {
	h := newAuthorHarness(t)
	ctx := context.Background()
	_, err := h.service.mutate(ctx, authorDictionary, func(ctx context.Context, m *session) error {
		state := reference.EntryState{
			ID:            "e1",
			Dictionary:    authorDictionary,
			CanonicalName: "Foreign Author",
			CanonicalKey:  "foreign author",
			Status:        reference.StatusCandidate,
		}
		entry := reference.RevivedEntry(state, m.now)
		draft := reference.NewDraft(authorDictionary)
		draft.Track(entry)
		_, err := m.record(ctx, draft, reference.ChangeReverted, entry.ID(), operatorMeta, 1)
		return err
	})

	if !errors.Is(err, reference.ErrInvalid) {
		t.Fatal("восстановление обошло проверку имени", err)
	}

	page, err := h.service.List(ctx, authorDictionary, reference.ListFilter{})

	if err != nil || page.Total != 0 || len(h.events(t)) != 0 {
		t.Fatal("неверное восстановление изменило реестр", page, err)
	}
}

func TestAuthorPolicyDoesNotRestrictOtherDictionaryJournalRestore(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	name := strings.Repeat("x", reference.DefaultMaxAliasLength+1)
	_, err := h.service.mutate(ctx, testDict, func(ctx context.Context, m *session) error {
		state := reference.EntryState{
			ID:            "e1",
			Dictionary:    testDict,
			CanonicalName: name,
			CanonicalKey:  name,
			Status:        reference.StatusConfirmed,
			Source:        reference.SourceOperator,
		}
		entry := reference.RevivedEntry(state, m.now)
		draft := reference.NewDraft(testDict)
		draft.Track(entry)
		_, err := m.record(ctx, draft, reference.ChangeReverted, entry.ID(), operatorMeta, 1)
		return err
	})

	if err != nil {
		t.Fatal("политика авторов изменила восстановление другого словаря", err)
	}

	page, err := h.service.List(ctx, testDict, reference.ListFilter{})

	if err != nil || page.Total != 1 || page.Entries[0].CanonicalName != name {
		t.Fatal("историческое имя другого словаря не восстановлено", page, err)
	}
}

func newAuthorHarness(t *testing.T) *harness {
	t.Helper()
	h := newHarness(t)
	h.store.SeedReferenceDictionary(reference.DictionaryState{
		Code:       authorDictionary,
		Kind:       reference.KindOpen,
		AliasKinds: reference.AliasKinds,
		Limits:     reference.DefaultLimits(),
	})
	return h
}
