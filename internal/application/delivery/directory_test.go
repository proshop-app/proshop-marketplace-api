package delivery_test

import (
	"context"
	"errors"
	"testing"
	"time"

	appdelivery "github.com/proshop-app/proshop-marketplace/internal/application/delivery"
	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/delivery"
)

var t0 = time.Date(2026, 10, 24, 10, 0, 0, 0, time.UTC)

type env struct {
	store     *fake.Store
	index     *fake.PickupIndex
	clock     *fake.Clock
	cdek      *fake.PickupProvider
	yandex    *fake.PickupProvider
	directory *appdelivery.Directory
}

func point(carrier delivery.Carrier, code, city string, lat, lon float64) delivery.PickupPoint {
	p, err := delivery.NewPickupPoint(
		carrier,
		code,
		"Пункт "+code,
		"ул. Примерная, "+code,
		city,
		delivery.Location{Lat: lat, Lon: lon},
		"Пн-Вс 10-20",
		nil,
		t0,
	)
	if err != nil {
		panic(err)
	}

	return p
}

func setup() *env {
	e := &env{store: fake.NewStore(), index: fake.NewPickupIndex(), clock: fake.NewClock(t0)}
	e.cdek = &fake.PickupProvider{
		Info: delivery.CarrierInfo{Code: "cdek", Name: "СДЭК"},
		Batches: [][]delivery.PickupPoint{
			{point("cdek", "A1", "Город", 55.70, 37.60), point("cdek", "A2", "Город", 55.80, 37.70)},
			{point("cdek", "B1", "Другой", 59.90, 30.30)},
		},
	}
	e.yandex = &fake.PickupProvider{
		Info: delivery.CarrierInfo{Code: "yandex", Name: "Яндекс Доставка"},
		Batches: [][]delivery.PickupPoint{
			{point("yandex", "Y1", "Город", 55.75, 37.65)},
		},
	}
	e.directory = appdelivery.NewDirectory(
		e.store,
		[]ports.PickupPointsProvider{e.cdek, e.yandex},
		e.index,
		e.store,
		e.clock,
		"pod-1",
	)

	return e
}

func TestCarriersAndRefresh(t *testing.T) {
	e := setup()
	ctx := context.Background()

	if carriers := e.directory.Carriers(); len(carriers) != 2 || carriers[0].Code != "cdek" ||
		carriers[1].Name != "Яндекс Доставка" {
		t.Fatalf("перевозчики по порядку подключения: %+v", carriers)
	}

	results := e.directory.Refresh(ctx)
	if len(results) != 2 || results[0].Fetched != 3 || results[0].Err != nil || results[1].Fetched != 1 {
		t.Fatalf("обновление: %+v", results)
	}
	if e.index.Size() != 4 {
		t.Fatalf("индекс наполнен: %d", e.index.Size())
	}

	found, err := e.directory.Search(
		ctx,
		"cdek",
		delivery.Area{BBox: &delivery.BBox{MinLon: 37.5, MinLat: 55.6, MaxLon: 37.8, MaxLat: 55.9}},
		0,
	)
	if err != nil || len(found) != 2 {
		t.Fatalf("пункты в области карты — только выбранного перевозчика: %+v %v", found, err)
	}

	byCity, err := e.directory.Search(ctx, "cdek", delivery.Area{City: "другой"}, 10)
	if err != nil || len(byCity) != 1 || byCity[0].Code != "B1" {
		t.Fatalf("пункты города: %+v %v", byCity, err)
	}

	_, err = e.directory.Search(ctx, "ozon", delivery.Area{City: "Город"}, 10)
	if !errors.Is(err, delivery.ErrUnknownCarrier) {
		t.Fatalf("неподключённый перевозчик: %v", err)
	}
	if _, err := e.directory.Search(ctx, "cdek", delivery.Area{}, 10); !errors.Is(err, delivery.ErrInvalidArea) {
		t.Fatalf("без области: %v", err)
	}

	_, err = e.directory.Search(ctx, "cdek", delivery.Area{City: "Город"}, 5000)
	if !errors.Is(err, delivery.ErrInvalidArea) {
		t.Fatalf("слишком много пунктов: %v", err)
	}

	if p, err := e.directory.Point(ctx, "yandex", "Y1"); err != nil || p.City != "Город" {
		t.Fatalf("пункт из справочника: %+v %v", p, err)
	}
}

func TestRefreshClosesVanishedPoints(t *testing.T) {
	e := setup()
	ctx := context.Background()

	e.directory.Refresh(ctx)

	e.clock.Advance(24 * time.Hour)
	e.cdek.Batches = [][]delivery.PickupPoint{{point("cdek", "A1", "Город", 55.70, 37.60)}}

	results := e.directory.Refresh(ctx)
	if results[0].Deactivated != 2 || results[0].Err != nil {
		t.Fatalf("исчезнувшие пункты закрываются: %+v", results[0])
	}
	if _, err := e.directory.Point(ctx, "cdek", "B1"); !errors.Is(err, delivery.ErrPickupPointNotFound) {
		t.Fatalf("закрытый пункт не выбирается: %v", err)
	}
	if e.index.Size() != 2 {
		t.Fatalf("закрытые убраны из индекса: %d", e.index.Size())
	}
}

func TestBrokenFetchClosesNothing(t *testing.T) {
	e := setup()
	ctx := context.Background()

	e.directory.Refresh(ctx)
	e.clock.Advance(24 * time.Hour)
	e.cdek.Err = errors.New("обрыв выгрузки")

	results := e.directory.Refresh(ctx)
	if results[0].Err == nil || results[0].Deactivated != 0 {
		t.Fatalf("при обрыве выгрузки ничего не закрывается: %+v", results[0])
	}
	if p, ok := e.store.PickupPoint("cdek", "B1"); !ok || !p.Active {
		t.Fatalf("пункт из непришедшей пачки остаётся: %+v", p)
	}
}

func TestRefreshSkipsLeasedCarrier(t *testing.T) {
	e := setup()
	ctx := context.Background()

	if ok, _ := e.store.Acquire(ctx, "pickup-points:cdek", "pod-2", time.Hour); !ok {
		t.Fatal("аренда другого процесса")
	}

	results := e.directory.Refresh(ctx)
	if !results[0].Skipped || results[0].Fetched != 0 || results[1].Skipped {
		t.Fatalf("обновление, которое ведёт другой процесс, пропускается: %+v", results)
	}
}

func TestEnsureIndexFillsNewIndex(t *testing.T) {
	e := setup()
	ctx := context.Background()

	e.store.SeedPickupPoint(point("cdek", "A1", "Город", 55.7, 37.6))

	if err := e.directory.EnsureIndex(ctx); err != nil || e.index.Size() != 1 {
		t.Fatalf("новый индекс наполняется из базы: %d %v", e.index.Size(), err)
	}

	e.store.SeedPickupPoint(point("cdek", "A2", "Город", 55.8, 37.7))
	if err := e.directory.EnsureIndex(ctx); err != nil || e.index.Size() != 1 {
		t.Fatalf("существующий индекс не переносится заново: %d %v", e.index.Size(), err)
	}
}
