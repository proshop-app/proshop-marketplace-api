package platformapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/delivery"
)

func TestPickupCatalogLargerThanResponseLimit(t *testing.T) {
	const total = 20000
	calls := 0
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		offset, _ := strconv.Atoi(r.URL.Query().Get("cursor"))
		count := min(512, total-offset)
		points := make([]delivery.PickupPoint, count)
		for i := range points {
			points[i] = delivery.PickupPoint{
				Carrier:    "cdek",
				Code:       strconv.Itoa(offset + i),
				Name:       "Пункт выдачи",
				Address:    strings.Repeat("Адрес ", 30),
				City:       "Город",
				RegionCode: "RU-MOW",
				Location:   delivery.Location{Lat: 55, Lon: 37},
			}
		}
		answer := ports.PickupPoints{Available: true, Items: points, Total: new(int)}
		*answer.Total = total
		if offset+count < total {
			cursor := strconv.Itoa(offset + count)
			answer.NextCursor = &cursor
		}
		if err := json.NewEncoder(w).Encode(answer); err != nil {
			t.Error(err)
		}
	})
	client := New(Config{BaseURL: "https://shop.example.test", Timeout: time.Minute, Conns: 4})
	client.http.Transport = handlerTransport(handler)
	answer, err := client.PickupPoints(context.Background(), "t1", "cdek", "Город")
	if err != nil || len(answer.Items) != total || calls != 40 {
		t.Fatal(len(answer.Items), calls, err)
	}
	bytes, err := json.Marshal(answer)
	if err != nil || len(bytes) <= 4*1024*1024 {
		t.Fatal(len(bytes), err)
	}
	for i, point := range answer.Items {
		if point.Code != strconv.Itoa(i) {
			t.Fatal(i, point.Code)
		}
	}
}

func TestPickupCatalogChangeRestartsFromFirstPage(t *testing.T) {
	firstPages, restarts := 0, 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("cursor") == "next" {
			if restarts == 0 {
				restarts++
				w.WriteHeader(http.StatusConflict)
				_, _ = w.Write([]byte(`{"code":"MARKETPLACE_PICKUP_CATALOG_CHANGED","message":"changed"}`))
				return
			}
		} else {
			firstPages++
		}
		code := "new-1"
		var next *string
		if firstPages == 1 {
			code = "old-1"
		}
		if r.URL.Query().Get("cursor") == "next" {
			code = "new-2"
		} else {
			value := "next"
			next = &value
		}
		total := 2
		answer := ports.PickupPoints{
			Available:  true,
			Total:      &total,
			NextCursor: next,
			Items: []delivery.PickupPoint{{
				Carrier:    "cdek",
				Code:       code,
				Name:       "ПВЗ",
				Address:    "Адрес",
				City:       "Город",
				RegionCode: "RU-MOW",
				Location:   delivery.Location{Lat: 55, Lon: 37},
			}},
		}
		if err := json.NewEncoder(w).Encode(answer); err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()
	client := New(Config{BaseURL: server.URL, Timeout: time.Second, Conns: 4})
	result, err := client.PickupPoints(context.Background(), "t1", "cdek", "Город")
	if err != nil || firstPages != 2 || restarts != 1 || len(result.Items) != 2 ||
		result.Items[0].Code != "new-1" || result.Items[1].Code != "new-2" {
		t.Fatal(result, firstPages, restarts, err)
	}
}
