package catalog

import "testing"

func TestSalesTierBoundaries(t *testing.T) {
	for _, tc := range []struct {
		count int64
		tier  uint8
	}{
		{0, 0},
		{1, 1},
		{9, 1},
		{10, 2},
		{99, 2},
		{100, 3},
		{1000, 3},
	} {
		if tier := salesTier(tc.count); tier != tc.tier {
			t.Fatalf("продаж: %d, ступень: %d, ожидалась %d", tc.count, tier, tc.tier)
		}
	}
}
