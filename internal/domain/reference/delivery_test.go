package reference

import (
	"errors"
	"strings"
	"testing"
)

var testChecksum = strings.Repeat("ab", 32)

func TestValidateDelivery(t *testing.T) {
	valid := Delivery{
		Dictionary: "b1",
		Version:    1,
		Checksum:   testChecksum,
		Entries: []DeliveryEntry{
			{Canonical: "Бренд", Status: StatusConfirmed, Aliases: []DeliveryAlias{{Text: "Brend"}}},
			{Canonical: "Бренд Два", Status: StatusCandidate},
		},
	}
	if err := ValidateDelivery(valid); err != nil {
		t.Fatal(err)
	}
	broken := map[string]func(d *Delivery){
		"номер":       func(d *Delivery) { d.Version = 0 },
		"сумма":       func(d *Delivery) { d.Checksum = strings.ToUpper(testChecksum) },
		"пусто":       func(d *Delivery) { d.Entries = nil },
		"статус":      func(d *Delivery) { d.Entries[1].Status = StatusMerged },
		"общий ключ":  func(d *Delivery) { d.Entries[1].Aliases = []DeliveryAlias{{Text: "BREND"}} },
		"вид":         func(d *Delivery) { d.Entries[1].Aliases = []DeliveryAlias{{Text: "x", Kind: "other"}} },
		"словарь":     func(d *Delivery) { d.Dictionary = "B 1" },
		"направления": func(d *Delivery) { d.Entries[0].Metadata = []byte(`{"directions":"d1"}`) },
	}
	for name, breakIt := range broken {
		d := valid
		d.Entries = append([]DeliveryEntry(nil), valid.Entries...)
		breakIt(&d)
		if err := ValidateDelivery(d); !errors.Is(err, ErrInvalidDelivery) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestPlanDelivery(t *testing.T) {
	d := Delivery{Version: 2, Checksum: testChecksum}
	if apply, err := PlanDelivery(nil, d); !apply || err != nil {
		t.Fatal("первая поставка не применяется")
	}
	latest := &DeliveryRecord{Version: 2, Checksum: testChecksum}
	if apply, err := PlanDelivery(latest, d); apply || err != nil {
		t.Fatal("повтор поставки не пустой")
	}
	changed := d
	changed.Checksum = strings.Repeat("cd", 32)
	if _, err := PlanDelivery(latest, changed); !errors.Is(err, ErrDeliveryConflict) {
		t.Fatal("подмена данных прошла")
	}
	older := d
	older.Version = 1
	if _, err := PlanDelivery(latest, older); !errors.Is(err, ErrDeliveryConflict) {
		t.Fatal("понижение номера прошло")
	}
	latest.RolledBack = true
	if _, err := PlanDelivery(latest, d); !errors.Is(err, ErrDeliveryConflict) {
		t.Fatal("повтор откаченной поставки прошёл")
	}
}

func TestPlanDeliveryRollback(t *testing.T) {
	d := Delivery{Version: 1, Checksum: testChecksum}
	dict := RestoreDictionary(DictionaryState{Code: "b1", Revision: 5})
	latest := &DeliveryRecord{Version: 1, Checksum: testChecksum, RevisionFrom: 2, RevisionTo: 5}
	if apply, err := PlanDeliveryRollback(latest, dict, d); !apply || err != nil {
		t.Fatalf("откат: %v %v", apply, err)
	}
	edited := RestoreDictionary(DictionaryState{Code: "b1", Revision: 6})
	if _, err := PlanDeliveryRollback(latest, edited, d); !errors.Is(err, ErrDeliveryConflict) {
		t.Fatal("откат после правок прошёл")
	}
	latest.RolledBack = true
	if apply, err := PlanDeliveryRollback(latest, edited, d); apply || err != nil {
		t.Fatal("повтор отката не пустой")
	}
	if _, err := PlanDeliveryRollback(nil, dict, d); !errors.Is(err, ErrDeliveryConflict) {
		t.Fatal("откат без поставки прошёл")
	}
}

func TestDictionaryAdvanceLimit(t *testing.T) {
	dict := RestoreDictionary(DictionaryState{Code: "b1", Limits: Limits{MaxEntries: 1}, Revision: 3})
	if err := dict.Advance(4, 1); err != nil {
		t.Fatal(err)
	}
	if err := dict.Advance(5, 1); !errors.Is(err, ErrLimit) {
		t.Fatalf("лимит записей: %v", err)
	}
	if err := dict.Advance(4, 0); err == nil {
		t.Fatal("ревизия не выросла, а словарь сдвинулся")
	}
}
