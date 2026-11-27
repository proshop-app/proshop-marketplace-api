package searchapi

import (
	"bytes"
	"encoding/json"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

func toDocument(doc ports.OfferDocument) (offerDocument, error) {
	if doc.Tile != nil {
		tile, err := encodeTile(doc.Tile)
		if err != nil {
			return offerDocument{}, err
		}
		return offerDocument{
			TenantID:  doc.TenantID,
			ProductID: doc.ProductID,
			Version:   doc.Version,
			Visible:   len(tile.Members) > 0,
			UpdatedAt: doc.UpdatedAt.UTC(),
			Tile:      tile,
		}, nil
	}
	attributes, err := encodeAttributes(doc.Attributes)

	if err != nil {
		return offerDocument{}, err
	}
	for index := range attributes {
		values, present := doc.AttributeFacetValues[attributes[index].Key]
		if present {
			if attributes[index].Type != "" {
				return offerDocument{}, ErrInvalid.WithMessage("facetValues требуют строковую характеристику")
			}
			if err := taxonomy.ValidateFacetValues(values); err != nil {
				return offerDocument{}, ErrInvalid.Wrap(err)
			}
			attributes[index].FacetValues = append([]string(nil), values...)
		}
		terms := doc.AttributeSearchTerms[attributes[index].Key]
		if err := taxonomy.ValidateSearchTerms(terms); err != nil {
			return offerDocument{}, ErrInvalid.Wrap(err)
		}
		attributes[index].SearchTerms = append([]string(nil), terms...)
	}
	if doc.SalesTier > 3 {
		return offerDocument{}, ErrInvalid.WithMessage("некорректная ступень продаж")
	}
	if doc.Rating != nil {
		rating, err := catalog.NormalizeRating(doc.Rating.Value, doc.Rating.Count)
		if err != nil {
			return offerDocument{}, err
		}
		doc.Rating = nil
		if rating.Count > 0 {
			doc.Rating = &rating
		}
	}
	out := offerDocument{
		SalesTier:         doc.SalesTier,
		Rating:            doc.Rating,
		CategoryID:        doc.CategoryID,
		CategoryPath:      doc.CategoryPath,
		Attributes:        attributes,
		TenantID:          doc.TenantID,
		ProductID:         doc.ProductID,
		SellerSlug:        doc.SellerSlug,
		SellerName:        doc.SellerName,
		Delivery:          doc.Delivery.Clone(),
		Name:              doc.Name,
		Description:       doc.Description,
		Image:             doc.Image,
		Assets:            append([]string{}, doc.Assets...),
		URL:               doc.URL,
		SKU:               doc.SKU,
		VariantsID:        doc.VariantsID,
		TileKey:           doc.TileKey,
		Variants:          doc.Variants,
		ModelKey:          doc.ModelKey,
		ModelKeySource:    doc.ModelKeySource,
		VariantKey:        doc.VariantKey,
		Price:             doc.Price,
		BasePrice:         doc.BasePrice,
		Available:         doc.Available,
		InStock:           doc.InStock,
		Visible:           doc.Visible,
		Listed:            doc.Listed,
		CheckoutAvailable: doc.CheckoutAvailable,
		UpdatedAt:         doc.UpdatedAt.UTC(),
		Version:           doc.Version,
	}

	for _, c := range doc.Categories {
		out.Categories = append(out.Categories, category(c))
	}

	return out, nil
}

func fromDocument(doc offerDocument) (ports.OfferDocument, error) {
	attributes, err := decodeAttributes(doc.Attributes)

	if err != nil {
		return ports.OfferDocument{}, err
	}
	terms, err := attributeSearchTerms(doc.Attributes)
	if err != nil {
		return ports.OfferDocument{}, err
	}
	identity := ids.OfferID{
		Tenant:  ids.TenantID(doc.TenantID),
		Product: ids.ProductID(doc.ProductID),
	}
	out := ports.OfferDocument{
		AttributeSearchTerms: terms,
		Rating:               doc.Rating,
		CategoryID:           doc.CategoryID,
		CategoryPath:         doc.CategoryPath,
		Attributes:           attributes,
		ID:                   identity.String(),
		TenantID:             doc.TenantID,
		ProductID:            doc.ProductID,
		SellerSlug:           doc.SellerSlug,
		SellerName:           doc.SellerName,
		Delivery:             doc.Delivery.Clone(),
		Name:                 doc.Name,
		Description:          doc.Description,
		Image:                doc.Image,
		Assets:               append([]string{}, doc.Assets...),
		URL:                  doc.URL,
		SKU:                  doc.SKU,
		VariantsID:           doc.VariantsID,
		TileKey:              doc.TileKey,
		Variants:             doc.Variants,
		ModelKey:             doc.ModelKey,
		ModelKeySource:       doc.ModelKeySource,
		VariantKey:           doc.VariantKey,
		Price:                doc.Price,
		BasePrice:            doc.BasePrice,
		Available:            doc.Available,
		InStock:              doc.InStock,
		Visible:              doc.Visible,
		Listed:               doc.Listed,
		CheckoutAvailable:    doc.CheckoutAvailable,
		UpdatedAt:            doc.UpdatedAt,
		Version:              doc.Version,
	}

	for _, c := range doc.Categories {
		out.Categories = append(out.Categories, catalog.Category(c))
	}

	return out, nil
}

func encodeTile(doc *ports.TileDocument) (*tileDocument, error) {
	tile := &tileDocument{
		Key:     doc.Key,
		Members: make([]map[string]json.RawMessage, 0, len(doc.Members)),
	}
	for _, member := range doc.Members {
		wire, err := toDocument(member)
		if err != nil {
			return nil, err
		}
		data, err := json.Marshal(wire)
		if err != nil {
			return nil, ErrInvalid.Wrap(err)
		}
		var values map[string]json.RawMessage
		if err := json.Unmarshal(data, &values); err != nil {
			return nil, ErrInvalid.Wrap(err)
		}
		if tile.Base == nil {
			tile.Base = values
			tile.Members = append(tile.Members, map[string]json.RawMessage{})
			continue
		}
		for field, base := range tile.Base {
			value, ok := values[field]
			if !ok {
				values[field] = json.RawMessage("null")
			} else if bytes.Equal(value, base) {
				delete(values, field)
			}
		}
		tile.Members = append(tile.Members, values)
	}
	return tile, nil
}
