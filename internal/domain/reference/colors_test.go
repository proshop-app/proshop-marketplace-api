package reference

import (
	"encoding/json"
	"testing"
)

func TestColorConfirmationAndMetadata(t *testing.T) {
	dict := RestoreDictionary(DictionaryState{
		Code:       "color",
		AliasKinds: AliasKinds,
		Limits:     DefaultLimits(),
	})
	if err := ValidateColorState(dict, EntryState{
		CanonicalName: "Хаки",
		Status:        StatusConfirmed,
	}); err == nil {
		t.Fatal("пустое поле Dictionary обошло проверку словаря")
	}

	entry, err := NewEntry(dict, NewEntryInput{Name: "Хаки", Status: StatusCandidate}, testNow)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := entry.ChangeStatus(StatusConfirmed, testNow); err != nil {
		t.Fatal(err)
	}
	if err := ValidateColorState(dict, entry.State()); err == nil {
		t.Fatal("подтверждён цвет без базового")
	}
	if _, err := entry.SetMetadata(json.RawMessage(`{"baseColors":["Зелёный"]}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := entry.ChangeStatus(StatusConfirmed, testNow); err != nil {
		t.Fatal(err)
	}
	if _, err := entry.SetMetadata(json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	if err := ValidateColorState(dict, entry.State()); err == nil {
		t.Fatal("удалена обязательная привязка")
	}
	for _, raw := range []string{
		`{"baseColors":["Хаки"]}`,
		`{"baseColors":["Синий","Синий"]}`,
		`{"baseColors":["Синий","Зелёный","Белый"]}`,
		`{"baseColors":"Зелёный"}`,
	} {
		if _, err := entry.SetMetadata(json.RawMessage(raw)); err != nil {
			t.Fatal(err)
		}
		if err := ValidateColorState(dict, entry.State()); err == nil {
			t.Errorf("приняты неверные родители: %s", raw)
		}
	}
	base, err := NewEntry(dict, NewEntryInput{Name: "Зелёный", Status: StatusConfirmed}, testNow)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := base.SetMetadata(json.RawMessage(`{"baseColors":["Синий"]}`)); err != nil {
		t.Fatal(err)
	}
	if err := ValidateColorState(dict, base.State()); err == nil {
		t.Fatal("у базового появились родители")
	}
}

func TestColorDeliveryValidation(t *testing.T) {
	delivery := Delivery{
		Dictionary: "color",
		Version:    1,
		Checksum:   "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		Entries:    []DeliveryEntry{{Canonical: "Хаки", Status: StatusConfirmed}},
	}
	if err := ValidateDelivery(delivery); err == nil {
		t.Fatal("поставка подтвердила непривязанный цвет")
	}
	delivery.Entries[0].Metadata = json.RawMessage(`{"baseColors":["Зелёный"]}`)
	if err := ValidateDelivery(delivery); err != nil {
		t.Fatal(err)
	}
}

func TestColorDeliveryFillsBindingWithoutReplacingMetadata(t *testing.T) {
	entry := RestoreEntry(EntryState{
		Dictionary:    "color",
		CanonicalName: "Хаки",
		Status:        StatusConfirmed,
		Metadata:      json.RawMessage(`{"order":42,"directions":["d1"]}`),
	})
	changed, err := entry.DeliveryMetadata(json.RawMessage(`{"order":1,"baseColors":["Зелёный"]}`))
	if err != nil || !changed {
		t.Fatalf("привязка не заполнена: %v", err)
	}
	if string(entry.Metadata()) != `{"baseColors":["Зелёный"],"directions":["d1"],"order":42}` {
		t.Fatalf("затёрты поля оператора: %s", entry.Metadata())
	}
	changed, err = entry.DeliveryMetadata(json.RawMessage(`{"baseColors":["Синий"]}`))
	if err != nil || changed {
		t.Fatal("поставка изменила выбор оператора")
	}
}
