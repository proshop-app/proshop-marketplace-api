package catalog_test

import (
	"math"
	"testing"

	"github.com/proshop-app/proshop-marketplace/internal/domain/catalog"
)

func TestRatingFollowsSnapshotRevision(t *testing.T) {
	first := snapshot(1, t0)
	first.AverageRating, first.RatingsCount = 4.5, 10
	product, err := catalog.NewProduct(first, t0)
	if err != nil {
		t.Fatal(err)
	}
	older := first
	older.AverageRating, older.RatingsCount = 1, 1
	if changed, err := product.Apply(older, t1); err != nil || changed {
		t.Fatal(changed, err)
	}
	if product.State().AverageRating != 4.5 || product.State().RatingsCount != 10 {
		t.Fatal(product.State())
	}
	newer := older
	newer.Revision++
	if changed, err := product.Apply(newer, t1); err != nil || !changed {
		t.Fatal(changed, err)
	}
	if product.State().AverageRating != 1 || product.State().RatingsCount != 1 {
		t.Fatal(product.State())
	}
	if catalog.ProductRating(5, 0) != nil {
		t.Fatal("рейтинг без оценок")
	}
	bad := first
	bad.AverageRating = math.NaN()
	if _, err := catalog.NewProduct(bad, t0); err == nil {
		t.Fatal("NaN принят как рейтинг")
	}
}

func TestSnapshotNormalizesRating(t *testing.T) {
	for _, tc := range []struct{ value, want float64 }{
		{-1, 0},
		{5.1, 5},
		{4.333, 4.3},
		{math.Inf(1), 5},
		{math.Inf(-1), 0},
	} {
		snap := snapshot(1, t0)
		snap.AverageRating, snap.RatingsCount = tc.value, 1
		product, err := catalog.NewProduct(snap, t0)
		if err != nil || product.State().AverageRating != tc.want {
			t.Fatal(product, err)
		}
	}
	snap := snapshot(1, t0)
	snap.RatingsCount = -1
	if _, err := catalog.NewProduct(snap, t0); err == nil {
		t.Fatal("отрицательное число оценок принято")
	}
}
