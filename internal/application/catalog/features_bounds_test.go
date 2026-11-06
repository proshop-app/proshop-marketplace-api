package catalog

import (
	"math"
	"testing"
)

func TestRecommendationPriceBounds(t *testing.T) {
	for _, tc := range []struct{ price, low, high int64 }{
		{1, 1, 1},
		{2, 2, 2},
		{3, 2, 4},
		{4, 3, 5},
		{10001, 6001, 14001},
		{math.MaxInt64, 5534023222112865485, math.MaxInt64},
	} {
		low, high := recommendationPriceBounds(tc.price)
		if low != tc.low || high != tc.high {
			t.Fatalf("%d: [%d, %d]", tc.price, low, high)
		}
	}
}
