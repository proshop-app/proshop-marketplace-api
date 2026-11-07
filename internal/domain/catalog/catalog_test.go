package catalog_test

import (
	"errors"
	"testing"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/domain/moderation"

	"github.com/proshop-app/proshop-marketplace/internal/domain/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
	"github.com/proshop-app/proshop-marketplace/pkg/money"
)

var (
	t0 = time.Date(2026, 10, 23, 10, 0, 0, 0, time.UTC)
	t1 = t0.Add(time.Minute)
	t2 = t0.Add(2 * time.Minute)
)

var visibleSeller = catalog.Seller{Slug: "shop", Name: "Магазин", Visible: true, CheckoutAvailable: true}

func rub(kopecks int64) money.Money { return money.FromKopecks(kopecks) }

func ptr(m money.Money) *money.Money { return &m }

func snapshot(revision int64, at time.Time) catalog.Snapshot {
	return catalog.Snapshot{
		TenantID:  "t1",
		ProductID: "p1",
		Revision:  revision,
		At:        at,
		Card: catalog.Card{
			Name:        " Товар ",
			Visible:     true,
			Marketplace: &taxonomy.Card{CategoryID: "c1", Published: true},
			Publication: taxonomy.Publication{Published: true, CategoryID: "c1"},
		},
		Price:     rub(10000),
		Available: 5,
		PriceProfiles: []catalog.PriceProfile{
			{
				Key:            catalog.DefaultPriceProfile,
				Price:          rub(10000),
				EffectivePrice: rub(10000),
			},
		},
	}
}

func TestProductAppliesHigherRevisionOnly(t *testing.T) {
	product, err := catalog.NewProduct(snapshot(2, t0), t0)
	if err != nil {
		t.Fatal(err)
	}
	if product.State().Card.Name != "Товар" || product.Version() != 1 {
		t.Fatalf("неожиданное состояние: %+v", product.State())
	}
	product.MarkSaved()

	for _, revision := range []int64{1, 2} {
		if changed, err := product.Apply(snapshot(revision, t1), t1); err != nil || changed {
			t.Fatalf("ревизия %d не новее: changed=%v err=%v", revision, changed, err)
		}
	}
	next := snapshot(3, t1)
	next.Price = rub(12000)
	if changed, err := product.Apply(next, t1); err != nil || !changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	if product.State().Price.Kopecks() != 12000 || product.Version() != 2 {
		t.Fatalf("неожиданное состояние: %+v", product.State())
	}
}

func TestProductDeleteAndResurrect(t *testing.T) {
	product, err := catalog.NewProduct(snapshot(1, t0), t0)
	if err != nil {
		t.Fatal(err)
	}
	product.MarkSaved()
	deletion := catalog.Snapshot{TenantID: "t1", ProductID: "p1", Revision: 2, Deleted: true, At: t1}
	if changed, err := product.Apply(deletion, t1); err != nil || !changed {
		t.Fatalf("удаление: changed=%v err=%v", changed, err)
	}
	if state := product.State(); !state.Deleted || state.Card.Name != "" || !state.Price.IsZero() {
		t.Fatalf("удалённый товар без карточки: %+v", state)
	}

	if changed, err := product.Apply(snapshot(3, t2), t2); err != nil || !changed || product.State().Deleted {
		t.Fatalf("восстановление: changed=%v err=%v", changed, err)
	}
}

func TestProductRejectsInvalidSnapshot(t *testing.T) {
	cases := map[string]func(*catalog.Snapshot){
		"нулевая ревизия":  func(s *catalog.Snapshot) { s.Revision = 0 },
		"нет времени":      func(s *catalog.Snapshot) { s.At = time.Time{} },
		"нет названия":     func(s *catalog.Snapshot) { s.Card.Name = "  " },
		"цена меньше нуля": func(s *catalog.Snapshot) { s.Price = rub(-1) },
		"промо меньше нуля": func(s *catalog.Snapshot) {
			s.PromoPrice = ptr(rub(-1))
		},
	}

	for name, mutate := range cases {
		s := snapshot(1, t0)
		mutate(&s)
		if _, err := catalog.NewProduct(s, t0); !errors.Is(err, catalog.ErrInvalidSnapshot) {
			t.Fatalf("%s: ожидалось ErrInvalidSnapshot, получено %v", name, err)
		}
	}

	product, _ := catalog.NewProduct(snapshot(1, t0), t0)
	other := snapshot(2, t1)
	other.ProductID = "p2"
	if _, err := product.Apply(other, t1); !errors.Is(err, catalog.ErrInvalidSnapshot) {
		t.Fatalf("снимок другого товара: получено %v", err)
	}
}

func TestSignalsOrderedByTime(t *testing.T) {
	signals := catalog.NewSignals("t1", "p1")

	if changed, err := signals.ApplyStock(catalog.StockSignal{Available: 3, At: t1}, t1); err != nil || !changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	if changed, _ := signals.ApplyStock(catalog.StockSignal{Available: 9, At: t0}, t1); changed {
		t.Fatal("старый остаток не применяется")
	}
	if changed, _ := signals.ApplyStock(catalog.StockSignal{Available: 3, At: t1}, t1); changed {
		t.Fatal("повтор не меняет сигналы")
	}
	if changed, _ := signals.ApplyStock(catalog.StockSignal{Available: -4, At: t2}, t2); !changed ||
		signals.State().Available != 0 {
		t.Fatalf("отрицательный остаток приводится к нулю: %+v", signals.State())
	}

	if changed, err := signals.ApplyPrice(catalog.PriceSignal{Price: rub(500), At: t1}, t1); err != nil || !changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	if changed, _ := signals.ApplyPrice(catalog.PriceSignal{Price: rub(700), At: t0}, t1); changed {
		t.Fatal("старая цена не применяется")
	}

	_, err := signals.ApplyPrice(catalog.PriceSignal{Price: rub(-1), At: t2}, t2)
	if !errors.Is(err, catalog.ErrInvalidSignal) {
		t.Fatalf("ожидалось ErrInvalidSignal, получено %v", err)
	}
	if _, err := signals.ApplyStock(catalog.StockSignal{Available: 1}, t2); !errors.Is(err, catalog.ErrInvalidSignal) {
		t.Fatalf("ожидалось ErrInvalidSignal, получено %v", err)
	}
}

func TestComposeOfferPrices(t *testing.T) {
	s := snapshot(1, t1)
	s.PriceProfiles[0].EffectivePrice = rub(8000)
	product, _ := catalog.NewProduct(s, t1)
	offer := catalog.ComposeOffer(product, nil, visibleSeller, allowedVerdict())
	if offer.Price.Kopecks() != 8000 || offer.BasePrice.Kopecks() != 10000 {
		t.Fatalf("промо после разрешения типа цены: %+v", offer)
	}
	signals := catalog.NewSignals("t1", "p1")
	_, _ = signals.ApplyPrice(catalog.PriceSignal{Price: rub(12000), At: t2}, t2)
	if got := catalog.ComposeOffer(product, signals, visibleSeller, allowedVerdict()); got.Price.Kopecks() != 8000 {
		t.Fatalf("цены из событий каталога/склада не заменяют Pricing: %+v", got)
	}

	s = snapshot(2, t2)
	s.PriceProfiles[0].EffectivePrice = rub(0)
	if _, err := product.Apply(s, t2); err != nil {
		t.Fatal(err)
	}
	if got := catalog.ComposeOffer(product, nil, visibleSeller, allowedVerdict()); !got.Price.IsZero() {
		t.Fatalf("ноль — действительная цена: %+v", got)
	}
}

func TestComposeOfferStock(t *testing.T) {
	product, _ := catalog.NewProduct(snapshot(1, t1), t1)

	offer := catalog.ComposeOffer(product, nil, visibleSeller, allowedVerdict())
	if offer.Available != 5 || !offer.InStock {
		t.Fatalf("остаток снимка: %+v", offer)
	}
	fresh := catalog.NewSignals("t1", "p1")
	_, _ = fresh.ApplyStock(catalog.StockSignal{Available: 0, At: t2}, t2)
	offer = catalog.ComposeOffer(product, fresh, visibleSeller, allowedVerdict())
	if offer.Available != 0 || offer.InStock {
		t.Fatalf("свежий остаток побеждает: %+v", offer)
	}
}

func TestComposeOfferVisibility(t *testing.T) {
	product, _ := catalog.NewProduct(snapshot(1, t1), t1)
	offer := catalog.ComposeOffer(product, nil, visibleSeller, allowedVerdict())
	if !offer.Visible || !offer.Listed || !offer.CheckoutAvailable {
		t.Fatalf("видимое предложение: %+v", offer)
	}
	hidden := catalog.ComposeOffer(
		product,
		nil,
		catalog.Seller{Visible: false, CheckoutAvailable: true},
		allowedVerdict(),
	)
	if hidden.Visible || hidden.Listed || hidden.CheckoutAvailable {
		t.Fatalf("скрытый продавец скрывает предложение: %+v", hidden)
	}
	onboarding := catalog.ComposeOffer(product, nil, catalog.Seller{Visible: true}, allowedVerdict())
	if !onboarding.Visible || onboarding.CheckoutAvailable {
		t.Fatalf("продавец без оплаты: виден, но без checkout: %+v", onboarding)
	}
	variant := snapshot(1, t1)
	variant.Card.OnlyVariant = true
	variantProduct, _ := catalog.NewProduct(variant, t1)
	offer = catalog.ComposeOffer(variantProduct, nil, visibleSeller, allowedVerdict())
	if !offer.Visible || !offer.Listed {
		t.Fatalf("флаг витрины продавца не скрывает предложение площадки: %+v", offer)
	}
	off := snapshot(1, t1)
	off.Card.Visible = false
	offProduct, _ := catalog.NewProduct(off, t1)
	if offer := catalog.ComposeOffer(offProduct, nil, visibleSeller, allowedVerdict()); offer.Visible {
		t.Fatalf("выключенный в магазине товар скрыт: %+v", offer)
	}

	deleted, _ := catalog.NewProduct(
		catalog.Snapshot{TenantID: "t1", ProductID: "p1", Revision: 1, Deleted: true, At: t1},
		t1,
	)
	offer = catalog.ComposeOffer(deleted, nil, visibleSeller, allowedVerdict())
	if offer.Visible || offer.ID.String() != "t1~p1" {
		t.Fatalf("удалённый товар скрыт: %+v", offer)
	}

	if offer := catalog.ComposeOffer(nil, nil, visibleSeller, allowedVerdict()); offer.Visible {
		t.Fatal("без товара предложения нет")
	}
}

func allowedVerdict() *moderation.Verdict {
	return moderation.RestoreVerdict(moderation.VerdictState{Status: moderation.Allowed, Version: 1})
}

func TestProductCopiesAssets(t *testing.T) {
	first := snapshot(1, t0)
	first.Card.Assets = []string{"main", "detail"}
	product, err := catalog.NewProduct(first, t0)
	if err != nil {
		t.Fatal(err)
	}
	first.Card.Assets[0] = "changed"
	if product.State().Card.Assets[0] != "main" {
		t.Fatal("снимок изменил галерею агрегата")
	}
	next := snapshot(2, t1)
	next.Card.Assets = []string{"next"}
	if _, err := product.Apply(next, t1); err != nil {
		t.Fatal(err)
	}
	next.Card.Assets[0] = "changed"
	state := product.State()
	restored := catalog.RestoreProduct(state)
	state.Card.Assets[0] = "changed"
	if restored.State().Card.Assets[0] != "next" {
		t.Fatal("восстановленная галерея разделяет срез")
	}
}
