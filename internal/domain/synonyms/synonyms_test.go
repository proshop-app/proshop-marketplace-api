package synonyms

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestDirectionAndWordBoundaries(t *testing.T) {
	source := Source{Kind: TaxonomyName, ID: "section", Revision: "17:"}
	first, ok := NewNomination("  USB-Хабы ", "Адаптеры", "section", source)
	same, normalized := NewNomination("usb-хабы", "адаптеры", "section", source)
	inverse, reversed := NewNomination(first.To, first.From, "section", source)
	cross, other := NewNomination(first.From, first.To, "another", source)
	if !ok || !normalized || !reversed || !other || first != same || first.ID == inverse.ID || first.ID == cross.ID {
		t.Fatalf("потеряна идентичность направления: %+v, %+v, %+v, %+v", first, same, inverse, cross)
	}
	if !first.Blocked(map[string]bool{"usb": true}) || first.Blocked(map[string]bool{"хаб": true}) {
		t.Fatal("стоп-лист должен совпадать по границе слова")
	}
	if _, ok := NewNomination("Ёлки", "елки", "section", source); ok {
		t.Fatal("схлопнувшееся направление принято")
	}
	if _, ok := NewNomination(strings.Repeat("Я", 65), "елки", "section", source); ok {
		t.Fatal("превышение длины принято")
	}
}

func TestNormalizedStopList(t *testing.T) {
	out, err := NormalizeBlocked([]string{" Ёлка ", "USB", "елка"})
	if err != nil || !reflect.DeepEqual(out, []string{"usb", "елка"}) {
		t.Fatalf("нормализация: %+v, %v", out, err)
	}
	if _, err := NormalizeBlocked(make([]string, MaxBlocked+1)); !errors.Is(err, ErrInvalid) {
		t.Fatalf("превышение потолка: %v", err)
	}
	if _, err := NormalizeBlocked([]string{"\x00браслет"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("управляющий символ: %v", err)
	}
}

func TestLifecycleGuardsActivationAndVersion(t *testing.T) {
	now := time.Now()
	n, _ := NewNomination("Первое имя", "Второе имя", "section", Source{})
	entry := NewEntry(n, now)
	if _, err := entry.Activate(now); !errors.Is(err, ErrConflict) {
		t.Fatalf("активация исходного кандидата: %v", err)
	}
	step, err := entry.Quarantine(now)
	if err != nil || step.To != Quarantined || entry.Version != 2 {
		t.Fatalf("карантин: %+v, %v", step, err)
	}
	if _, err := entry.Activate(now); !errors.Is(err, ErrConflict) {
		t.Fatalf("активация без проверки: %v", err)
	}
	entry.Record(Check{Outcome: Pass}, now)
	if step, err := entry.Activate(now); err != nil || step.To != Active || entry.Version != 4 {
		t.Fatalf("активация: %+v, %v", step, err)
	}
	if entry.ActivatedAt == nil || !entry.NextCheck.Equal(now.Add(RecheckInterval)) {
		t.Fatalf("дата активации и повторной проверки: %+v", entry)
	}
	step, err = entry.RejectActive(Check{Outcome: Reject}, now)
	if err != nil || step.From != Active || step.To != Disabled || entry.Version != 5 ||
		entry.LastCheck.Outcome != Reject {
		t.Fatalf("отрицательная проверка: %+v, %+v, %v", step, entry, err)
	}
	if _, err := entry.Quarantine(now); !errors.Is(err, ErrConflict) {
		t.Fatalf("отключённая пара вернулась в очередь: %v", err)
	}
}
