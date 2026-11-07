package buyer_test

import (
	"testing"

	"github.com/proshop-app/proshop-marketplace/internal/domain/buyer"
	"github.com/proshop-app/proshop-marketplace/pkg/money"
	"github.com/proshop-app/proshop-marketplace/pkg/regions"
)

func TestProfileChangesAndCopies(t *testing.T) {
	p := buyer.RestoreProfile(buyer.ProfileState{})
	state := buyer.ProfileState{Name: " Имя ", Region: &buyer.Region{Code: "RU-MOW", Name: "Подмена"}}
	changed, err := p.Change(state)
	name, _ := regions.Name("RU-MOW")
	if err != nil || !changed || p.State().Name != "Имя" || p.State().Region.Name != name {
		t.Fatalf("изменение: %+v, %v", p.State(), err)
	}
	state.Region.Code = "bad"
	if p.State().Region.Code != "RU-MOW" {
		t.Fatal("внешний указатель изменил агрегат")
	}
	p.MarkSaved()
	if changed, err := p.Change(p.State()); err != nil || changed {
		t.Fatal("повтор меняет версию")
	}
	copy := p.State()
	copy.Region.Code = "bad"
	if _, err := p.Change(copy); err == nil {
		t.Fatal("неизвестный регион принят")
	}
	if p.State().Version != 1 || p.OriginVersion() != 1 {
		t.Fatal("версия")
	}
}

func TestPriceDropBoundary(t *testing.T) {
	initial, target := money.FromKopecks(100), money.FromKopecks(80)
	if buyer.PriceDropped(initial, initial, nil) {
		t.Fatal("равная цена не упала")
	}
	if !buyer.PriceDropped(initial, money.FromKopecks(99), nil) {
		t.Fatal("снижение на копейку")
	}
	if !buyer.PriceDropped(initial, target, &target) {
		t.Fatal("цель включительна")
	}
	if buyer.PriceDropped(initial, money.FromKopecks(81), &target) {
		t.Fatal("цель не достигнута")
	}
}

func TestPriceDropTargetAboveInitial(t *testing.T) {
	initial, target := money.FromKopecks(100), money.FromKopecks(120)
	for _, current := range []int64{100, 110, 120} {
		if buyer.PriceDropped(initial, money.FromKopecks(current), &target) {
			t.Fatal(current)
		}
	}
	if !buyer.PriceDropped(initial, money.FromKopecks(99), &target) {
		t.Fatal("снижение потеряно")
	}
}
