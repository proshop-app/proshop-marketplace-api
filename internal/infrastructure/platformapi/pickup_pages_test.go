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
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Query().Get("page") != "1" {
			t.Error("ожидалась внутренняя страница")
		}
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
	}))
	defer server.Close()
	client := New(Config{BaseURL: server.URL, Timeout: time.Second})
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
