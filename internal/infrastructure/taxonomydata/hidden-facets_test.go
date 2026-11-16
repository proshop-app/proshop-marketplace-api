package taxonomydata

import (
	"context"
	"slices"
	"testing"
	"time"

	appcatalog "github.com/proshop-app/proshop-marketplace/internal/application/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
)

func TestBundledHiddenFacets(t *testing.T) {
	release, _, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	store := fake.NewStore()
	store.SeedTaxonomy(release.Definition())
	queries := appcatalog.NewQueries(store, fake.NewIndex(), nil, nil, nil, fake.NewClock(time.Time{}))
	for _, id := range []string{"shoes", "clothing", "electronics"} {
		t.Run(id, func(t *testing.T) {
			query, err := queries.PrepareQuery(context.Background(), ports.OfferQuery{CategoryID: id})
			if err != nil {
				t.Fatal(err)
			}
			codes := []string{}
			for _, field := range query.FacetFields {
				codes = append(codes, field.Code)
			}
			if slices.Contains(codes, "model") != (id == "electronics") {
				t.Errorf("неверная видимость модели: %v", codes)
			}
			if id == "shoes" && (!slices.Contains(codes, "color") || !slices.Contains(codes, "size_footwear_label")) {
				t.Errorf("потеряны цвет или размер: %v", codes)
			}
		})
	}
}

func TestBundledHiddenFacetLeafCoverage(t *testing.T) {
	release, _, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	for _, root := range release.Categories {
		t.Run(root.ID, func(t *testing.T) {
			leaves, hidden := 0, 0
			var walk func(taxonomy.Category)
			walk = func(node taxonomy.Category) {
				if len(node.Children) == 0 {
					leaves++
					if slices.Contains(release.Schemas[node.Schema].HiddenFacets, "model") {
						hidden++
					}
					return
				}
				for _, child := range node.Children {
					walk(child)
				}
			}
			walk(root)
			want := 0
			switch root.ID {
			case "shoes":
				want = 163
			case "clothing":
				want = 333
			}
			if hidden != want || want > 0 && hidden != leaves {
				t.Fatalf("скрыто %d/%d листьев, ожидается %d", hidden, leaves, want)
			}
		})
	}
}
