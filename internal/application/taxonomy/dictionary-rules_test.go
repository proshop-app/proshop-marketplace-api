package taxonomy_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	apptaxonomy "github.com/proshop-app/proshop-marketplace/internal/application/taxonomy"
	"github.com/proshop-app/proshop-marketplace/internal/domain/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/domain/reference"
	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
)

func hintRuleStore(t *testing.T) (*fake.Store, *apptaxonomy.Service, taxonomy.Definition) {
	t.Helper()
	store := fake.NewStore()
	current := stringDefinition(5, "a1")
	current.Attributes = append(current.Attributes, taxonomy.Attribute{
		Code:       "e1",
		Name:       "Список",
		Type:       "enum",
		Dictionary: "e1",
		Values:     []string{"v1"},
		ValuesMeta: map[string]taxonomy.ValueMeta{"v1": {Hex: "#112233"}},
	})
	store.SeedTaxonomy(current)
	for _, code := range []string{"a1", "d1", "d2"} {
		store.SeedReferenceDictionary(reference.DictionaryState{
			Code:     code,
			Kind:     reference.KindOpen,
			Identity: code == "a1",
			Limits:   reference.DefaultLimits(),
		})
	}
	store.SeedReferenceDictionary(reference.DictionaryState{Code: "e1", Kind: reference.KindClosed})
	service := apptaxonomy.NewService(store, fake.NewClock(time.Now()), nil)
	return store, service, current
}

func TestHintRuleCRUDPreservesIdentityEnumsProductsAndSemanticNoops(t *testing.T) {
	ctx := context.Background()
	store, service, initial := hintRuleStore(t)
	product := catalog.ProductState{
		TenantID:  "t1",
		ProductID: "p1",
		Version:   7,
		Card: catalog.Card{Publication: taxonomy.Publication{
			Version:    5,
			Published:  true,
			Attributes: map[string]json.RawMessage{"a1": json.RawMessage(`"Неизвестное"`)},
		}},
	}
	store.SeedProduct(product)
	rule := taxonomy.DictionaryBinding{AttributeCode: "a1", Mode: "extend", Dictionaries: []string{"d1", "d2"}}
	version, err := service.PutDictionaryBinding(ctx, rule, 5)
	if err != nil || version != 6 {
		t.Fatal(version, err)
	}
	current, _ := service.Current(ctx)
	savedProduct, _ := store.Product("t1", "p1")
	if !reflect.DeepEqual(current.Attributes, initial.Attributes) || !reflect.DeepEqual(savedProduct, product) ||
		len(store.OutboxRows()) != 0 {
		t.Fatal("hint-only команда изменила identity, enum, товар или outbox", current, savedProduct)
	}
	rule.Dictionaries = []string{"d2", "d1"}
	if version, err := service.PutDictionaryBinding(ctx, rule, 6); err != nil || version != 6 {
		t.Fatal("перестановка множества изменила версию", version, err)
	}
	current, _ = service.Current(ctx)
	if !reflect.DeepEqual(current.DictionaryBindings[0].Dictionaries, []string{"d1", "d2"}) {
		t.Fatal("no-op переставил wire order", current.DictionaryBindings)
	}
	disabled := taxonomy.DictionaryBinding{
		CategoryID:    "c1",
		AttributeCode: "a1",
		Mode:          "replace",
		Dictionaries:  []string{},
	}
	if version, err := service.PutDictionaryBinding(ctx, disabled, 6); err != nil || version != 7 {
		t.Fatal(version, err)
	}
	current, _ = service.Current(ctx)
	if len(current.HintDictionaries("c1", "a1")) != 0 || current.Attributes[0].Dictionary != "a1" {
		t.Fatal("replace[] изменил базовую identity-привязку", current)
	}
	if version, err := service.DeleteDictionaryBinding(ctx, "c1", "a1", 7); err != nil || version != 8 {
		t.Fatal(version, err)
	}
	if version, err := service.DeleteDictionaryBinding(ctx, "c1", "a1", 8); err != nil || version != 8 {
		t.Fatal("удаление отсутствующего правила изменило версию", version, err)
	}
	current, _ = service.Current(ctx)
	if !reflect.DeepEqual(current.HintDictionaries("c1", "a1"), []string{"a1", "d1", "d2"}) {
		t.Fatal("удаление не восстановило наследование", current.DictionaryBindings)
	}
	if _, err := service.PutDictionaryBinding(ctx, disabled, 5); !errors.Is(err, taxonomy.ErrConflict) {
		t.Fatal("устаревшая CAS принята", err)
	}
}

func TestHintRuleErrorsAndFailedCommitAreAtomic(t *testing.T) {
	for _, code := range []string{"missing", "e1"} {
		store, service, initial := hintRuleStore(t)
		_, err := service.PutDictionaryBinding(
			context.Background(),
			taxonomy.DictionaryBinding{
				AttributeCode: "a1",
				Mode:          "replace",
				Dictionaries:  []string{code},
			}, 5)
		if !errors.Is(err, taxonomy.ErrInvalid) {
			t.Fatal("неизвестный или закрытый источник принят", code, err)
		}
		current, _ := service.Current(context.Background())
		if !reflect.DeepEqual(current, initial) || len(store.OutboxRows()) != 0 {
			t.Fatal("частичная публикация", current)
		}
	}
	store, service, initial := hintRuleStore(t)
	store.FailCommit = errors.New("сбой фиксации")
	if _, err := service.PutDictionaryBinding(
		context.Background(),
		taxonomy.DictionaryBinding{
			AttributeCode: "a1",
			Mode:          "replace",
			Dictionaries:  []string{"d1"},
		}, 5); err == nil {
		t.Fatal("сбой фиксации скрыт")
	}
	store.FailCommit = nil
	current, _ := service.Current(context.Background())
	if !reflect.DeepEqual(current, initial) {
		t.Fatal("rule пережило rollback", current)
	}
}

func TestConcurrentHintRulesHaveOneCASWinner(t *testing.T) {
	_, service, _ := hintRuleStore(t)
	var group sync.WaitGroup
	results := make(chan error, 2)
	for _, code := range []string{"d1", "d2"} {
		group.Add(1)
		go func(dictionary string) {
			defer group.Done()
			_, err := service.PutDictionaryBinding(
				context.Background(),
				taxonomy.DictionaryBinding{
					AttributeCode: "a1",
					Mode:          "replace",
					Dictionaries:  []string{dictionary},
				}, 5)
			results <- err
		}(code)
	}
	group.Wait()
	close(results)
	winners, conflicts := 0, 0
	for err := range results {
		switch {
		case err == nil:
			winners++
		case errors.Is(err, taxonomy.ErrConflict):
			conflicts++
		default:
			t.Fatal(err)
		}
	}
	if winners != 1 || conflicts != 1 {
		t.Fatal(winners, conflicts)
	}
}

func TestRemoveHintUsesHistoryAndPreservesCurrentClosedValues(t *testing.T) {
	ctx := context.Background()
	store, service, current := hintRuleStore(t)
	old := current
	old.Version = 4
	old.Attributes = append([]taxonomy.Attribute(nil), current.Attributes...)
	old.Attributes[0].Dictionary = ""
	old.Attributes[1].Values = []string{"v1"}
	store.SeedTaxonomy(old)
	current.Attributes[1].Values = []string{
		"v1",
		"v2",
		"v3",
		"v4",
		"v5",
		"v6",
		"v7",
		"v8",
		"v9",
		"v10",
		"v11",
		"v12",
		"v13",
	}
	current.DictionaryBindings = []taxonomy.DictionaryBinding{
		{CategoryID: "c1", AttributeCode: "a1", Mode: "extend", Dictionaries: []string{"d1"}},
	}
	store.SeedTaxonomy(current)
	version, err := service.RemoveHint(ctx, apptaxonomy.RemoveHintCommand{
		Attribute:       "a1",
		Dictionary:      "a1",
		PreviousVersion: 4,
		ExpectedVersion: 5,
	})
	if err != nil || version != 6 {
		t.Fatal(version, err)
	}
	saved, _ := service.Current(ctx)
	current.Version = 6
	current.Attributes[0].Dictionary = ""
	if !reflect.DeepEqual(saved, current) || len(saved.Attributes[1].Values) != 13 || len(store.OutboxRows()) != 0 {
		t.Fatal("исторический снимок затёр текущие данные", saved)
	}
	// У исходно привязанного поля нет исторического доказательства пустого dictionary.
	if _, err := service.RemoveHint(ctx, apptaxonomy.RemoveHintCommand{
		Attribute:       "a1",
		Dictionary:      "a1",
		PreviousVersion: 5,
		ExpectedVersion: 6,
	}); !errors.Is(err, taxonomy.ErrConflict) {
		t.Fatal("неверная история принята", err)
	}
}

func TestReleasePreservesAbsentRulesAndRejectsDanglingScopes(t *testing.T) {
	ctx := context.Background()
	store := fake.NewStore()
	current := stringDefinition(1, "a1")
	current.DictionaryBindings = []taxonomy.DictionaryBinding{
		{CategoryID: "c1", AttributeCode: "a1", Mode: "extend", Dictionaries: []string{"d1"}},
	}
	store.SeedTaxonomy(current)
	for _, code := range []string{"a1", "d1"} {
		store.SeedReferenceDictionary(reference.DictionaryState{Code: code, Kind: reference.KindOpen})
	}
	service := apptaxonomy.NewService(store, fake.NewClock(time.Now()), nil)
	release := bindingRelease()
	release.DictionaryBindings = nil
	if _, err := service.Apply(ctx, release); err != nil {
		t.Fatal(err)
	}
	next, _ := service.Current(ctx)
	if !reflect.DeepEqual(next.DictionaryBindings, current.DictionaryBindings) {
		t.Fatal("старый формат поставки удалил rules", next)
	}
	release.Version++
	release.ExpectedVersion = next.Version
	release.Categories = []taxonomy.Category{{ID: "c2", Name: "Другая", Slug: "c2", Schema: "s1"}}
	if _, err := service.Apply(ctx, release); !errors.Is(err, taxonomy.ErrInvalid) {
		t.Fatal("поставка молча удалила dangling rule", err)
	}
	release.Categories = current.Categories
	release.DictionaryBindings = []taxonomy.DictionaryBinding{}
	if _, err := service.Apply(ctx, release); err != nil {
		t.Fatal("явная пустая поставка rules отклонена", err)
	}
	next, _ = service.Current(ctx)
	if len(next.DictionaryBindings) != 0 {
		t.Fatal("явный пустой набор rules проигнорирован", next)
	}
}
