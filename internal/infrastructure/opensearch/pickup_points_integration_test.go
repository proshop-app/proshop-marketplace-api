//go:build integration

package opensearch_test

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/delivery"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/opensearch"
)

func pickupIndex(t *testing.T) *opensearch.PickupIndex {
	t.Helper()

	name := fmt.Sprintf("test-pickups-%d", time.Now().UnixNano())
	index := opensearch.NewPickupIndex(testIndex(t), name)

	t.Cleanup(func() {
		request, _ := http.NewRequest(http.MethodDelete, clusterURL()+"/"+name, nil)
		if response, err := http.DefaultClient.Do(request); err == nil {
			_ = response.Body.Close()
		}
	})

	return index
}

func pickup(carrier delivery.Carrier, code, city string, lat, lon float64) delivery.PickupPoint {
	p, err := delivery.NewPickupPoint(
		carrier,
		code,
		"Пункт "+code,
		"ул. Примерная, "+code,
		city,
		delivery.Location{Lat: lat, Lon: lon},
		"Пн-Пт",
		nil,
		time.Now(),
	)
	if err != nil {
		panic(err)
	}

	return p
}

func TestPickupIndex(t *testing.T) {
	index := pickupIndex(t)
	ctx := context.Background()

	if created, err := index.Ensure(ctx); err != nil || !created {
		t.Fatalf("новый индекс: %v %v", created, err)
	}
	if created, err := index.Ensure(ctx); err != nil || created {
		t.Fatalf("существующий индекс не пересоздаётся: %v %v", created, err)
	}

	points := []delivery.PickupPoint{
		pickup("cdek", "A1", "Город", 55.70, 37.60),
		pickup("cdek", "A2", "Город", 55.80, 37.70),
		pickup("cdek", "B1", "Другой", 59.90, 30.30),
		pickup("yandex", "Y1", "Город", 55.75, 37.65),
	}
	if err := index.Put(ctx, points); err != nil {
		t.Fatal(err)
	}
	if err := index.RefreshPickups(ctx); err != nil {
		t.Fatal(err)
	}

	box := &delivery.BBox{MinLon: 37.5, MinLat: 55.6, MaxLon: 37.75, MaxLat: 55.85}
	found, err := index.Search(ctx, ports.PickupPointQuery{Carrier: "cdek", Area: delivery.Area{BBox: box}, Limit: 10})
	if err != nil || len(found) != 2 || found[0].Code != "A1" || found[0].Location.Lat != 55.70 {
		t.Fatalf("пункты СДЭК в области, без пунктов другого перевозчика: %+v %v", found, err)
	}

	byCity, err := index.Search(
		ctx,
		ports.PickupPointQuery{Carrier: "cdek", Area: delivery.Area{City: "другой"}, Limit: 10},
	)
	if err != nil || len(byCity) != 1 || byCity[0].Code != "B1" {
		t.Fatalf("город без учёта регистра: %+v %v", byCity, err)
	}

	limited, _ := index.Search(
		ctx,
		ports.PickupPointQuery{Carrier: "cdek", Area: delivery.Area{City: "Город"}, Limit: 1},
	)
	if len(limited) != 1 {
		t.Fatalf("предел ответа: %d", len(limited))
	}

	closed := points[0]
	closed.Active = false
	missing := pickup("cdek", "ZZ", "Город", 55.7, 37.6)
	missing.Active = false

	if err := index.Put(ctx, []delivery.PickupPoint{closed, missing}); err != nil {
		t.Fatalf("удаление закрытых, отсутствующий — не ошибка: %v", err)
	}
	_ = index.RefreshPickups(ctx)

	query := ports.PickupPointQuery{Carrier: "cdek", Area: delivery.Area{BBox: box}, Limit: 10}
	if found, _ := index.Search(ctx, query); len(found) != 1 {
		t.Fatalf("закрытый пункт ушёл из индекса: %+v", found)
	}
}
