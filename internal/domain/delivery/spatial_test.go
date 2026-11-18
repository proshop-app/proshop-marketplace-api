package delivery_test

import (
	"fmt"
	"testing"

	"github.com/proshop-app/proshop-marketplace/internal/domain/delivery"
)

func spatialPoints(count int) []delivery.PickupPoint {
	points := make([]delivery.PickupPoint, count)
	for n := range points {
		points[n] = delivery.PickupPoint{
			Code:       fmt.Sprint(n),
			RegionCode: "RU-MOW",
			Location:   delivery.Location{Lat: 55 + float64(n/1000)*0.001, Lon: 37 + float64(n%1000)*0.001},
		}
	}
	return points
}

func TestPickupAreaIsBoundedAndRegionScoped(t *testing.T) {
	points := spatialPoints(100000)
	points[0].RegionCode = "RU-MOS"
	index := delivery.NewPickupIndex(points)
	area := index.Area(delivery.Bounds{54, 36, 57, 39}, []string{"RU-MOW"})
	if area.Total != 99999 || len(area.Items) != delivery.PickupMapLimit || !area.Refine {
		t.Fatal(area.Total, len(area.Items), area.Refine)
	}
	for _, point := range area.Items {
		if point.RegionCode != "RU-MOW" {
			t.Fatal(point)
		}
	}
	small := index.Area(delivery.Bounds{55, 37, 55.001, 37.001}, []string{"RU-MOW"})
	if small.Total != 3 || len(small.Items) != 3 || small.Refine {
		t.Fatal(small)
	}
	outside := index.Area(delivery.Bounds{0, 0, 1, 1}, []string{"RU-MOW"})
	if outside.Total != 0 || len(outside.Items) != 0 {
		t.Fatal(outside)
	}
}

func BenchmarkPickupArea(b *testing.B) {
	for _, count := range []int{10000, 100000} {
		b.Run(fmt.Sprint(count), func(b *testing.B) {
			index := delivery.NewPickupIndex(spatialPoints(count))
			box := delivery.Bounds{55, 37, 55.009, 37.009}
			b.ResetTimer()
			for n := 0; n < b.N; n++ {
				index.Area(box, []string{"RU-MOW"})
			}
		})
	}
}

// Все 20 000 точек находятся в одной ячейке: плотный центр ~600 × 600 м.
func denseSpatialPoints() []delivery.PickupPoint {
	points := make([]delivery.PickupPoint, 20000)
	for n := range points {
		points[n] = delivery.PickupPoint{
			Code:       fmt.Sprint(n),
			RegionCode: "RU-MOW",
			Location: delivery.Location{
				Lat: 55.741 + float64(n/200)*0.00005,
				Lon: 37.601 + float64(n%200)*0.00005,
			},
		}
	}
	return points
}

func TestDensePickupAreaCountsBoundaryCellWithoutTruncation(t *testing.T) {
	index := delivery.NewPickupIndex(denseSpatialPoints())
	narrow := index.Area(delivery.Bounds{55.74301, 37.60501, 55.74399, 37.60599}, []string{"RU-MOW"})
	if narrow.Total != 361 || len(narrow.Items) != 361 || narrow.Refine {
		t.Fatal(narrow.Total, len(narrow.Items), narrow.Refine)
	}
	wide := index.Area(delivery.Bounds{55.7405, 37.6005, 55.7465, 37.6115}, []string{"RU-MOW"})
	if wide.Total != 20000 || len(wide.Items) != delivery.PickupMapLimit || !wide.Refine {
		t.Fatal(wide.Total, len(wide.Items), wide.Refine)
	}
}

func BenchmarkDensePickupArea(b *testing.B) {
	index := delivery.NewPickupIndex(denseSpatialPoints())
	for _, test := range []struct {
		name string
		box  delivery.Bounds
	}{
		{
			name: "narrow",
			box:  delivery.Bounds{55.74301, 37.60501, 55.74399, 37.60599},
		},
		{
			name: "wide",
			box:  delivery.Bounds{55.7405, 37.6005, 55.7465, 37.6115},
		},
	} {
		b.Run(test.name, func(b *testing.B) {
			b.ReportAllocs()
			for n := 0; n < b.N; n++ {
				index.Area(test.box, []string{"RU-MOW"})
			}
		})
	}
}
