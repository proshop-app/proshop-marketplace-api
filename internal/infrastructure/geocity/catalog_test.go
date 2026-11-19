package geocity

import (
	"crypto/sha256"
	"fmt"
	"math"
	"testing"
)

func TestEmbeddedReleaseIsPinnedAndSupportsExistingRegions(t *testing.T) {
	if sum := fmt.Sprintf("%x", sha256.Sum256([]byte(cityCSV))); sum !=
		"aefd11fd133881c348e0138b273a300ec6b3909c8f61d8d1d5ffc5e85021ec0d" {
		t.Fatal("изменена поставка: обновите источник, дату, checksum и проверку данных", sum)
	}
	catalog, err := New()
	if err != nil {
		t.Fatal(err)
	}
	count, regionCodes := 0, map[string]bool{}
	var visit func(*node)
	visit = func(n *node) {
		if n == nil {
			return
		}
		count++
		regionCodes[n.item.city.RegionCode] = true
		// В каждой записи есть полный договор ручного выбора.
		if n.item.city.City == "" || n.item.city.FiasID == "" || n.item.city.KladrID == "" {
			t.Fatal(n.item)
		}
		visit(n.left)
		visit(n.right)
	}
	visit(catalog.root)
	if count != 1100 || len(regionCodes) != 83 {
		t.Fatal(count, len(regionCodes))
	}
	if result := catalog.Nearest(0, 0); result.City != "" {
		t.Fatal(result)
	}
}

func TestTreeMatchesExhaustiveNearestOnSphere(t *testing.T) {
	catalog, err := New()
	if err != nil {
		t.Fatal(err)
	}
	for lat := -80.0; lat <= 80; lat += 8 {
		for lon := -180.0; lon <= 180; lon += 9 {
			point := vector(lat, lon)
			actual, distance := catalog.root.nearest(point, entry{}, math.Inf(1))
			expected, best := entry{}, math.Inf(1)
			var scan func(*node)
			scan = func(n *node) {
				if n == nil {
					return
				}
				if d := squaredDistance(point, n.item.point); d < best {
					best, expected = d, n.item
				}
				scan(n.left)
				scan(n.right)
			}
			scan(catalog.root)
			if distance != best || actual.city.KladrID != expected.city.KladrID {
				t.Fatal(lat, lon, actual, expected)
			}
		}
	}
}

func BenchmarkNearest(b *testing.B) {
	catalog, err := New()
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = catalog.Nearest(55.75, 37.62)
	}
}
