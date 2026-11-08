package catalog_test

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	appcatalog "github.com/proshop-app/proshop-marketplace/internal/application/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
)

func TestPrepareFacetSelectionPreservesDefaultsAndFilters(t *testing.T) {
	store := fake.NewStore()
	store.SeedTaxonomy(taxonomy.Definition{
		Attributes: []taxonomy.Attribute{{
			Code:   "a1",
			Type:   "enum",
			Values: []string{"v1"},
			Facet:  true,
		}},
	})
	index := &fake.PromotionIndex{}
	queries := appcatalog.NewQueries(store, index, nil, nil, nil, fake.NewClock(time.Time{}))
	empty := []ports.FacetCode{}
	partial := []ports.FacetCode{ports.FacetCategory, ports.FacetTenant}
	include, exclude := true, false
	for _, tc := range []struct {
		name       string
		facets     *[]ports.FacetCode
		attributes *bool
		wantFields int
	}{
		{
			name:       "старые запросы всегда полные",
			wantFields: 1,
		},
		{
			name:       "явное включение",
			attributes: &include,
			wantFields: 1,
		},
		{
			name:       "только базовые фасеты",
			attributes: &exclude,
		},
		{
			name:       "без любых фасетов",
			facets:     &empty,
			attributes: &exclude,
		},
		{
			name:       "частичные базовые и прежние атрибуты",
			facets:     &partial,
			wantFields: 1,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := queries.Search(context.Background(), ports.OfferQuery{
				Limit:           5,
				Facets:          tc.facets,
				AttributeFacets: tc.attributes,
				Attributes: map[string]ports.AttributeFilter{
					"a1": {Values: []string{"v1"}},
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			got := index.Query
			if !reflect.DeepEqual(got.Facets, tc.facets) || len(got.FacetFields) != tc.wantFields {
				t.Fatalf("выбор фасетов потерян: %+v", got)
			}
			if got.Attributes["a1"].Type != "enum" || got.PriceContexts != nil {
				t.Fatalf("изменены нормализация фильтра или гостевая цена: %+v", got)
			}
		})
	}
	unknown := []ports.FacetCode{"missing"}
	duplicate := []ports.FacetCode{ports.FacetPrice, ports.FacetPrice}
	for _, codes := range []*[]ports.FacetCode{&unknown, &duplicate} {
		_, err := queries.PrepareQuery(context.Background(), ports.OfferQuery{Facets: codes})
		if !errors.Is(err, appcatalog.ErrInvalidQuery) {
			t.Fatalf("неверные фасеты приняты: %v, %v", codes, err)
		}
	}
	_, err := queries.PrepareQuery(context.Background(), ports.OfferQuery{
		AttributeFacets: &exclude,
		Attributes: map[string]ports.AttributeFilter{
			"missing": {Values: []string{"v1"}},
		},
	})
	if !errors.Is(err, appcatalog.ErrInvalidQuery) {
		t.Fatalf("отключение фасетов отключило проверку фильтров: %v", err)
	}
}
