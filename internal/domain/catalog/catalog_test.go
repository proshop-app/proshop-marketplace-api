package catalog_test

import (
	"errors"
	"testing"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/domain/catalog"
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
		Card:      catalog.Card{Name: " Товар ", Visible: true, Tags: []string{"tag-1"}},
		Price:     rub(10000),
		Available: 5,
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
	s.PromoPrice = ptr(rub(8000))
	product, _ := catalog.NewProduct(s, t1)

	offer := catalog.ComposeOffer(product, nil, visibleSeller)
	if offer.Price.Kopecks() != 8000 || offer.BasePrice.Kopecks() != 10000 {
		t.Fatalf("промо ниже прайса — цена продажи: %+v", offer)
	}
	if offer.ID.String() != "t1~p1" {
		t.Fatalf("идентификатор %s", offer.ID)
	}

	stalePrice := catalog.NewSignals("t1", "p1")
	_, _ = stalePrice.ApplyPrice(catalog.PriceSignal{Price: rub(9000), At: t0}, t0)
	if offer := catalog.ComposeOffer(product, stalePrice, visibleSeller); offer.BasePrice.Kopecks() != 10000 ||
		offer.Price.Kopecks() != 8000 {
		t.Fatalf("сигнал старше снимка игнорируется: %+v", offer)
	}

	freshPrice := catalog.NewSignals("t1", "p1")
	_, _ = freshPrice.ApplyPrice(catalog.PriceSignal{Price: rub(12000), At: t2}, t2)
	if offer := catalog.ComposeOffer(product, freshPrice, visibleSeller); offer.BasePrice.Kopecks() != 12000 ||
		offer.Price.Kopecks() != 12000 {
		t.Fatalf("свежий прайс отменяет промо снимка: %+v", offer)
	}

	high := snapshot(1, t1)
	high.PromoPrice = ptr(rub(15000))
	highProduct, _ := catalog.NewProduct(high, t1)
	if offer := catalog.ComposeOffer(highProduct, nil, visibleSeller); offer.Price.Kopecks() != 10000 {
		t.Fatalf("промо выше прайса игнорируется: %+v", offer)
	}

	zero := snapshot(1, t1)
	zero.PromoPrice = ptr(rub(0))
	zeroProduct, _ := catalog.NewProduct(zero, t1)
	if offer := catalog.ComposeOffer(zeroProduct, nil, visibleSeller); offer.Price.Kopecks() != 10000 {
		t.Fatalf("нулевая промо-цена игнорируется: %+v", offer)
	}
}

func TestComposeOfferStock(t *testing.T) {
	product, _ := catalog.NewProduct(snapshot(1, t1), t1)

	if offer := catalog.ComposeOffer(product, nil, visibleSeller); offer.Available != 5 || !offer.InStock {
		t.Fatalf("остаток снимка: %+v", offer)
	}

	fresh := catalog.NewSignals("t1", "p1")
	_, _ = fresh.ApplyStock(catalog.StockSignal{Available: 0, At: t2}, t2)
	if offer := catalog.ComposeOffer(product, fresh, visibleSeller); offer.Available != 0 || offer.InStock {
		t.Fatalf("свежий остаток побеждает: %+v", offer)
	}
}

func TestComposeOfferVisibility(t *testing.T) {
	product, _ := catalog.NewProduct(snapshot(1, t1), t1)

	offer := catalog.ComposeOffer(product, nil, visibleSeller)
	if !offer.Visible || !offer.Listed || !offer.CheckoutAvailable {
		t.Fatalf("видимое предложение: %+v", offer)
	}

	hidden := catalog.ComposeOffer(product, nil, catalog.Seller{Visible: false, CheckoutAvailable: true})
	if hidden.Visible || hidden.Listed || hidden.CheckoutAvailable {
		t.Fatalf("скрытый продавец скрывает предложение: %+v", hidden)
	}

	onboarding := catalog.ComposeOffer(product, nil, catalog.Seller{Visible: true})
	if !onboarding.Visible || onboarding.CheckoutAvailable {
		t.Fatalf("продавец без оплаты: виден, но без checkout: %+v", onboarding)
	}

	variant := snapshot(1, t1)
	variant.Card.OnlyVariant = true
	variantProduct, _ := catalog.NewProduct(variant, t1)
	if offer := catalog.ComposeOffer(variantProduct, nil, visibleSeller); !offer.Visible || offer.Listed {
		t.Fatalf("«только вариант» открывается, но не в выдаче: %+v", offer)
	}

	off := snapshot(1, t1)
	off.Card.Visible = false
	offProduct, _ := catalog.NewProduct(off, t1)
	if offer := catalog.ComposeOffer(offProduct, nil, visibleSeller); offer.Visible {
		t.Fatalf("выключенный в магазине товар скрыт: %+v", offer)
	}

	deleted, _ := catalog.NewProduct(
		catalog.Snapshot{TenantID: "t1", ProductID: "p1", Revision: 1, Deleted: true, At: t1},
		t1,
	)
	if offer := catalog.ComposeOffer(deleted, nil, visibleSeller); offer.Visible || offer.ID.String() != "t1~p1" {
		t.Fatalf("удалённый товар скрыт: %+v", offer)
	}

	if offer := catalog.ComposeOffer(nil, nil, visibleSeller); offer.Visible {
		t.Fatal("без товара предложения нет")
	}
}

func TestTags(t *testing.T) {
	tag, err := catalog.NewTag(catalog.TagSnapshot{TenantID: "t1", TagID: "tag-1", Revision: 1, Value: " Новинка "}, t0)
	if err != nil || tag.State().Value != "Новинка" {
		t.Fatalf("%+v %v", tag, err)
	}
	tag.MarkSaved()

	stale := catalog.TagSnapshot{TenantID: "t1", TagID: "tag-1", Revision: 1, Value: "x"}
	if changed, _ := tag.Apply(stale, t1); changed {
		t.Fatal("та же ревизия не применяется")
	}

	deletion := catalog.TagSnapshot{TenantID: "t1", TagID: "tag-1", Revision: 2, Deleted: true}
	if changed, err := tag.Apply(deletion, t1); err != nil || !changed || tag.State().Value != "" {
		t.Fatalf("удаление: changed=%v err=%v state=%+v", changed, err, tag.State())
	}

	_, err = catalog.NewTag(catalog.TagSnapshot{TenantID: "t1", TagID: "tag-2", Revision: 1}, t0)
	if !errors.Is(err, catalog.ErrInvalidSnapshot) {
		t.Fatalf("тег без значения: получено %v", err)
	}

	labels := catalog.LabelTags([]string{"tag-1", "free-text"}, map[string]string{"tag-1": "Новинка"})
	if labels[0].Label != "Новинка" || labels[1].Label != "free-text" {
		t.Fatalf("подписи: %+v", labels)
	}
}
