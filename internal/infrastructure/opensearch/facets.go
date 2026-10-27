package opensearch

import (
	"encoding/json"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
)

// Даже при 128 строковых полях запрос остаётся ниже стандартных 65536 search.max_buckets.
const facetBucketLimit = 256

type priceHit struct {
	Hits struct {
		Hits []struct {
			Source struct {
				Price json.Number `json:"effectivePrice"`
			} `json:"_source"`
		} `json:"hits"`
	} `json:"hits"`
}

type priceProfileFacet struct {
	Count    int      `json:"doc_count"`
	MinPrice priceHit `json:"minPrice"`
	MaxPrice priceHit `json:"maxPrice"`
}

type facetAggregation struct {
	Selected *priceProfileFacet `json:"selected"`
	MinPrice priceHit           `json:"minPrice"`
	MaxPrice priceHit           `json:"maxPrice"`
	Count    int                `json:"doc_count"`
	Values   struct {
		Buckets []struct {
			Key         json.RawMessage `json:"key"`
			KeyAsString string          `json:"key_as_string"`
			Count       int             `json:"doc_count"`
		} `json:"buckets"`
		Count int          `json:"count"`
		Min   *json.Number `json:"min"`
		Max   *json.Number `json:"max"`
	} `json:"values"`
}

func facetAggregations(query ports.OfferQuery) map[string]any {
	aggs := map[string]any{}
	for code, field := range map[string]string{"category": "categoryId", "tenant": "tenantId", "inStock": "inStock"} {
		aggs[code] = map[string]any{
			"filter": map[string]any{"match_all": map[string]any{}},
			"aggs":   map[string]any{"values": termsAggregation(field)},
		}
	}
	aggs["price"] = map[string]any{
		"nested": map[string]any{"path": "priceProfiles"},
		"aggs": map[string]any{
			"selected": map[string]any{
				"filter": profileFilter(query),
				"aggs": map[string]any{
					"minPrice": priceBound("asc"),
					"maxPrice": priceBound("desc"),
				},
			},
		},
	}
	for _, a := range query.FacetFields {
		aggs["attr."+a.Code] = facetAggregationBody("attributes."+a.Code, a.Type)
	}
	return aggs
}

func termsAggregation(field string) map[string]any {
	return map[string]any{"terms": map[string]any{"field": field, "size": facetBucketLimit}}
}

func facetAggregationBody(field, kind string) map[string]any {
	values := termsAggregation(field)
	if kind == "number" {
		values = map[string]any{"stats": map[string]any{"field": field}}
	}
	return map[string]any{
		"filter": map[string]any{"exists": map[string]any{"field": field}},
		"aggs":   map[string]any{"values": values},
	}
}

func priceBound(order string) map[string]any {
	return map[string]any{"top_hits": map[string]any{
		"size":    1,
		"sort":    []any{map[string]any{"priceProfiles.effectivePrice": order}},
		"_source": true,
	}}
}

func readFacets(raw map[string]json.RawMessage, fields []taxonomy.Attribute) ([]ports.Facet, error) {
	definitions := []taxonomy.Attribute{
		{Code: "category", Name: "Категория площадки", Type: "enum"},
		{Code: "tenant", Name: "Магазин", Type: "enum"},
		{Code: "price", Name: "Цена", Type: "number"},
		{Code: "inStock", Name: "Наличие", Type: "boolean"},
	}
	for _, a := range fields {
		a.Code = "attr." + a.Code
		definitions = append(definitions, a)
	}
	out := make([]ports.Facet, 0, len(definitions))
	for _, a := range definitions {
		var agg facetAggregation
		if encoded, ok := raw[a.Code]; ok {
			if err := json.Unmarshal(encoded, &agg); err != nil {
				return nil, ErrUnavailable.Wrap(err)
			}
		}
		f := ports.Facet{
			Code:   a.Code,
			Name:   a.Name,
			Type:   a.Type,
			Unit:   a.Unit,
			Count:  agg.Count,
			Min:    agg.Values.Min,
			Max:    agg.Values.Max,
			Values: []ports.FacetBucket{},
		}
		if a.Code == "price" && agg.Selected != nil {
			agg.MinPrice = agg.Selected.MinPrice
			agg.MaxPrice = agg.Selected.MaxPrice
			f.Count = agg.Selected.Count
		}
		if a.Code == "price" {
			if len(agg.MinPrice.Hits.Hits) > 0 {
				n := agg.MinPrice.Hits.Hits[0].Source.Price
				f.Min = &n
			}
			if len(agg.MaxPrice.Hits.Hits) > 0 {
				n := agg.MaxPrice.Hits.Hits[0].Source.Price
				f.Max = &n
			}
		}
		for _, b := range agg.Values.Buckets {
			value := b.KeyAsString
			if value == "" {
				if json.Unmarshal(b.Key, &value) != nil {
					value = string(b.Key)
				}
			}
			f.Values = append(f.Values, ports.FacetBucket{Value: value, Count: b.Count})
		}
		out = append(out, f)
	}
	return out, nil
}
