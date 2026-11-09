package http

import (
	"encoding/json"
	"strconv"

	appcatalog "github.com/proshop-app/proshop-marketplace/internal/application/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/catalog"
)

type variantItemJSON struct {
	ID        string `json:"id"`
	Current   bool   `json:"current"`
	Value     string `json:"value"`
	Position  int    `json:"position"`
	Price     rubles `json:"price"`
	BasePrice rubles `json:"basePrice"`
	Available int    `json:"available"`
	InStock   bool   `json:"inStock"`
	Image     string `json:"image,omitempty"`
	Duplicate bool   `json:"duplicate,omitempty"`
}

type variantBlockJSON struct {
	GroupID   string              `json:"groupId"`
	Axis      catalog.VariantAxis `json:"axis"`
	Items     []variantItemJSON   `json:"items"`
	Truncated bool                `json:"truncated"`
}

type priceBucketJSON struct {
	From  rubles `json:"from"`
	To    rubles `json:"to"`
	Count int    `json:"count"`
}

type facetJSON struct {
	Buckets []priceBucketJSON   `json:"buckets,omitempty"`
	Code    string              `json:"code"`
	Name    string              `json:"name"`
	Type    string              `json:"type"`
	Unit    string              `json:"unit,omitempty"`
	Count   int                 `json:"count"`
	Values  []ports.FacetBucket `json:"values"`
	Min     any                 `json:"min,omitempty"`
	Max     any                 `json:"max,omitempty"`
}

func variantResponse(block *appcatalog.VariantBlock) *variantBlockJSON {
	if block == nil {
		return nil
	}
	result := &variantBlockJSON{
		GroupID:   block.GroupID,
		Axis:      block.Axis,
		Items:     make([]variantItemJSON, 0, len(block.Items)),
		Truncated: block.Truncated,
	}
	for _, item := range block.Items {
		result.Items = append(result.Items, variantItemJSON{
			ID:        item.ID,
			Current:   item.Current,
			Value:     item.Value,
			Position:  item.Position,
			Price:     rubles(item.Price),
			BasePrice: rubles(item.BasePrice),
			Available: item.Available,
			InStock:   item.InStock,
			Image:     item.Image,
			Duplicate: item.Duplicate,
		})
	}
	return result
}

func facetResponses(facets []ports.Facet) ([]facetJSON, error) {
	result := make([]facetJSON, 0, len(facets))
	for _, facet := range facets {
		item := facetJSON{
			Code:   facet.Code,
			Name:   facet.Name,
			Type:   facet.Type,
			Unit:   facet.Unit,
			Count:  facet.Count,
			Values: facet.Values,
		}
		if facet.Min != nil {
			item.Min = facet.Min
		}
		if facet.Max != nil {
			item.Max = facet.Max
		}
		if facet.Code == "price" {
			item.Unit = "RUB"
			var err error
			item.Min, err = priceBoundResponse(facet.Min)
			if err != nil {
				return nil, err
			}
			item.Max, err = priceBoundResponse(facet.Max)
			if err != nil {
				return nil, err
			}
			for _, bucket := range facet.Buckets {
				item.Buckets = append(item.Buckets, priceBucketJSON{
					From:  rubles(bucket.From),
					To:    rubles(bucket.To),
					Count: bucket.Count,
				})
			}
		}
		result = append(result, item)
	}
	return result, nil
}

func priceBoundResponse(value *json.Number) (any, error) {
	if value == nil {
		return nil, nil
	}
	kopecks, err := strconv.ParseInt(value.String(), 10, 64)
	if err != nil {
		return nil, err
	}
	return rubles(kopecks), nil
}
