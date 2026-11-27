package catalog_test

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	appcatalog "github.com/proshop-app/proshop-marketplace/internal/application/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
)

type resultPanelIndex struct {
	*fake.Index
	page  ports.OfferPage
	query ports.OfferQuery
	calls int
}

func (x *resultPanelIndex) Search(_ context.Context, query ports.OfferQuery) (ports.OfferPage, error) {
	x.query = query
	x.calls++
	return x.page, nil
}

func TestResultFacetPanelPresentationAndFilters(t *testing.T) {
	store := fake.NewStore()
	store.SeedTaxonomy(taxonomy.Definition{
		Version: 4,
		Attributes: []taxonomy.Attribute{
			{Code: "brand", Type: "string", Facet: true},
			{Code: "color", Type: "string", Facet: true},
		},
		Schemas: map[string]taxonomy.Schema{
			"s1": {
				Attributes: []string{"brand", "color"},
				FacetLayout: &taxonomy.FacetLayout{
					Primary:    []string{"brand"},
					Additional: []string{"color"},
				},
			},
			"s2": {Attributes: []string{"brand"}},
		},
		Categories: []taxonomy.Category{
			{ID: "one", Schema: "s1"},
			{ID: "two", Schema: "s2"},
		},
	})
	index := &resultPanelIndex{
		Index: fake.NewIndex(),
		page: ports.OfferPage{
			FacetCategoryID: "one",
			Facets: []ports.Facet{
				{Code: "attr.color", Values: []ports.FacetBucket{{Value: "v1", Count: 2}}},
				{Code: "attr.brand", Values: []ports.FacetBucket{{Value: "v2", Count: 2}}},
			},
		},
	}
	queries := appcatalog.NewQueries(store, index, nil, nil, nil, fake.NewClock(time.Time{}))
	ctx := context.Background()
	query := ports.OfferQuery{Text: "товар"}
	page, err := queries.Search(ctx, query)
	if err != nil || page.FacetCategoryID != "one" || index.calls != 1 ||
		index.query.CategoryID != "" || index.query.FacetTaxonomyVersion != 4 ||
		len(page.Facets) != 2 || page.Facets[0].Code != "attr.brand" || !page.Facets[0].Primary ||
		page.Facets[1].Code != "attr.color" || page.Facets[1].Primary {
		t.Fatalf("порядок и приоритет панели: %+v, запрос %+v, ошибка %v", page, index.query, err)
	}

	query.Attributes = map[string]ports.AttributeFilter{
		"color": {Values: []string{"v1"}},
	}
	query.Offset = 10
	index.page.FacetCategoryID = "two"
	page, err = queries.Search(ctx, query)
	if err != nil || page.FacetCategoryID != "two" || index.query.Attributes["color"].Type != "string" ||
		!slices.Contains(page.AvailableFacetKeys, "attr.color") || len(page.Facets) != 2 {
		t.Fatalf("сохранение выбранной характеристики после смены панели: %+v, %v", page, err)
	}

	query.FacetKeys = []string{"attr.color"}
	page, err = queries.Search(ctx, query)
	if err != nil || !slices.Contains(index.query.FacetKeys, "attr.color") ||
		index.query.FacetTaxonomyVersion != 4 || len(page.AvailableFacetKeys) != 0 {
		t.Fatalf("догрузка по ключу прежней панели: %+v, %v", page, err)
	}
	query.CategoryID = "two"
	if _, err := queries.Search(ctx, query); !errors.Is(err, appcatalog.ErrInvalidQuery) {
		t.Fatalf("явный раздел принял чужой фильтр: %v", err)
	}
}
