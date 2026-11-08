package catalog_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	appcatalog "github.com/proshop-app/proshop-marketplace/internal/application/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
	"github.com/proshop-app/proshop-marketplace/pkg/money"
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
	attributes := []ports.FacetCode{ports.FacetAttributes}
	withAttributes := []ports.FacetCode{ports.FacetCategory, ports.FacetAttributes}
	for _, tc := range []struct {
		name       string
		facets     *[]ports.FacetCode
		wantFields int
	}{
		{
			name:       "старые запросы всегда полные",
			wantFields: 1,
		},
		{
			name:       "только атрибутные",
			facets:     &attributes,
			wantFields: 1,
		},
		{
			name:   "без любых фасетов",
			facets: &empty,
		},
		{
			name:   "частичные базовые без атрибутов",
			facets: &partial,
		},
		{
			name:       "категория с атрибутами",
			facets:     &withAttributes,
			wantFields: 1,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := queries.Search(context.Background(), ports.OfferQuery{
				Limit:  5,
				Facets: tc.facets,
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
		Facets: &empty,
		Attributes: map[string]ports.AttributeFilter{
			"missing": {Values: []string{"v1"}},
		},
	})
	if !errors.Is(err, appcatalog.ErrInvalidQuery) {
		t.Fatalf("отключение фасетов отключило проверку фильтров: %v", err)
	}
}

func TestFacetSelectionPreservesCardsPricesSortAndPagination(t *testing.T) {
	store, index, platform := fake.NewStore(), fake.NewIndex(), fake.NewPlatform()
	for _, tenant := range []ids.TenantID{"t1", "t2"} {
		seedSeller(store, tenant, participant.TenantActive, true)
		platform.SetBuyerProfile("buyer", tenant, "marketplace:buyer")
	}
	store.SeedTaxonomy(taxonomy.Definition{
		Attributes: []taxonomy.Attribute{{
			Code:   "a1",
			Type:   "enum",
			Values: []string{"v1", "v2"},
			Facet:  true,
		}},
	})
	for i := 0; i < 6; i++ {
		tenant := []string{"t1", "t2"}[i%2]
		price, personal := money.FromKopecks(int64(100+i*10)), money.FromKopecks(int64(200-i*10))
		doc := ports.OfferDocument{
			ID:         fmt.Sprintf("%s~p%d", tenant, i),
			TenantID:   tenant,
			ProductID:  fmt.Sprintf("p%d", i),
			CategoryID: "c1",
			Name:       "Товар",
			Visible:    true,
			Listed:     true,
			InStock:    i%2 == 0,
			UpdatedAt:  t0.Add(time.Duration(i) * time.Minute),
			Version:    1,
			Rating:     &catalog.Rating{Value: 4.5, Count: int64(i + 1)},
			Attributes: map[string]json.RawMessage{"a1": json.RawMessage(`"v1"`)},
			PriceProfiles: []catalog.PriceProfile{
				{Key: catalog.DefaultPriceProfile, Price: price, EffectivePrice: price},
				{Key: "marketplace:buyer", Price: personal, EffectivePrice: personal},
			},
		}
		if err := index.Put(context.Background(), []ports.OfferDocument{doc}); err != nil {
			t.Fatal(err)
		}
	}
	queries := appcatalog.NewQueries(store, index, platform, nil, nil, fake.NewClock(time.Time{}))
	empty := []ports.FacetCode{}
	category := []ports.FacetCode{ports.FacetCategory}
	partial := []ports.FacetCode{ports.FacetTenant, ports.FacetAttributes}
	all := []ports.FacetCode{
		ports.FacetCategory,
		ports.FacetTenant,
		ports.FacetInStock,
		ports.FacetPrice,
		ports.FacetAttributes,
	}
	for _, buyer := range []ids.UUID{"", "buyer"} {
		for _, order := range []ports.OfferSort{
			ports.SortRelevance,
			ports.SortPriceAsc,
			ports.SortPriceDesc,
			ports.SortNewest,
			ports.SortRating,
		} {
			query := ports.OfferQuery{
				BuyerID: buyer,
				Sort:    order,
				Limit:   2,
				Offset:  1,
				Attributes: map[string]ports.AttributeFilter{
					"a1": {Values: []string{"v1"}},
				},
			}
			full, err := queries.Search(context.Background(), query)
			if err != nil || full.Total != 6 || len(full.Items) != 2 || len(full.Facets) != 5 {
				t.Fatalf("полный ответ: %+v, %v", full, err)
			}
			for _, selection := range []*[]ports.FacetCode{&empty, &category, &partial, &all} {
				query.Facets = selection
				page, err := queries.Search(context.Background(), query)
				if err != nil || page.Total != full.Total || !reflect.DeepEqual(page.Items, full.Items) {
					t.Fatalf("покупатель=%s порядок=%s выбор=%v: %+v, %v", buyer, order, selection, page, err)
				}
				wanted := []ports.Facet{}
				for _, facet := range full.Facets {
					code := ports.FacetCode(facet.Code)
					if strings.HasPrefix(facet.Code, "attr.") {
						code = ports.FacetAttributes
					}
					if query.WantsFacet(code) {
						wanted = append(wanted, facet)
					}
				}
				if !reflect.DeepEqual(page.Facets, wanted) {
					t.Fatalf("счётчики/границы выбранных фасетов изменились: %+v, ожидалось %+v", page.Facets, wanted)
				}
			}
		}
	}
}

func TestFacetSelectionAlwaysValidatesTaxonomy(t *testing.T) {
	store := fake.NewStore()
	store.SeedTaxonomy(taxonomy.Definition{
		Attributes: []taxonomy.Attribute{{
			Code:   "a1",
			Type:   "enum",
			Values: []string{"v1"},
			Facet:  true,
		}},
	})
	queries := appcatalog.NewQueries(store, fake.NewIndex(), nil, nil, nil, fake.NewClock(time.Time{}))
	none := []ports.FacetCode{}
	attributes := []ports.FacetCode{ports.FacetAttributes}
	for _, selection := range []*[]ports.FacetCode{nil, &none, &attributes} {
		for _, filters := range []map[string]ports.AttributeFilter{
			{"missing": {Values: []string{"v1"}}},
			{"a1": {Values: []string{"missing"}}},
			{"a1": {Min: "1"}},
		} {
			_, err := queries.PrepareQuery(context.Background(), ports.OfferQuery{
				Facets:     selection,
				Attributes: filters,
			})
			if !errors.Is(err, appcatalog.ErrInvalidQuery) {
				t.Fatalf("выбор=%v фильтр=%v: %v", selection, filters, err)
			}
		}
		_, err := queries.PrepareQuery(context.Background(), ports.OfferQuery{Facets: selection, CategoryID: "missing"})
		if !errors.Is(err, appcatalog.ErrInvalidQuery) {
			t.Fatalf("выбор отключил проверку категории: %v", err)
		}
	}
}
