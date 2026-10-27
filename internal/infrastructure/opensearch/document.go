package opensearch

import (
	"encoding/json"
	"time"

	"github.com/proshop-app/proshop-marketplace/pkg/money"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
)

// document — предложение в индексе; суммы — копейки.
type document struct {
	PriceProfiles     []priceProfileDocument       `json:"priceProfiles"`
	Delivery          participant.DeliverySettings `json:"delivery"`
	FreeDeliveryFrom  int64                        `json:"freeDeliveryFrom"`
	CategoryID        string                       `json:"categoryId,omitempty"`
	CategoryPath      []string                     `json:"categoryPath,omitempty"`
	Attributes        map[string]json.RawMessage   `json:"attributes,omitempty"`
	OfferID           string                       `json:"offerId"`
	TenantID          string                       `json:"tenantId"`
	ProductID         string                       `json:"productId"`
	SellerSlug        string                       `json:"sellerSlug,omitempty"`
	SellerName        string                       `json:"sellerName,omitempty"`
	Name              string                       `json:"name,omitempty"`
	Description       string                       `json:"description,omitempty"`
	Image             string                       `json:"image,omitempty"`
	URL               string                       `json:"url,omitempty"`
	SKU               string                       `json:"sku,omitempty"`
	Categories        []category                   `json:"categories,omitempty"`
	Tags              []string                     `json:"tags,omitempty"`
	VariantsID        string                       `json:"variantsId,omitempty"`
	Price             int64                        `json:"price"`
	BasePrice         int64                        `json:"basePrice"`
	Available         int                          `json:"available"`
	InStock           bool                         `json:"inStock"`
	Visible           bool                         `json:"visible"`
	Listed            bool                         `json:"listed"`
	CheckoutAvailable bool                         `json:"checkoutAvailable"`
	UpdatedAt         time.Time                    `json:"updatedAt"`
}

type category struct {
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
	URL  string `json:"url,omitempty"`
}

func toDocument(doc ports.OfferDocument) document {
	out := document{
		PriceProfiles:     priceProfileDocuments(doc),
		CategoryID:        doc.CategoryID,
		CategoryPath:      doc.CategoryPath,
		Attributes:        doc.Attributes,
		OfferID:           doc.ID,
		TenantID:          doc.TenantID,
		ProductID:         doc.ProductID,
		SellerSlug:        doc.SellerSlug,
		SellerName:        doc.SellerName,
		Delivery:          doc.Delivery.Clone(),
		FreeDeliveryFrom:  doc.FreeDeliveryFrom,
		Name:              doc.Name,
		Description:       doc.Description,
		Image:             doc.Image,
		URL:               doc.URL,
		SKU:               doc.SKU,
		Tags:              doc.Tags,
		VariantsID:        doc.VariantsID,
		Price:             doc.Price,
		BasePrice:         doc.BasePrice,
		Available:         doc.Available,
		InStock:           doc.InStock,
		Visible:           doc.Visible,
		Listed:            doc.Listed,
		CheckoutAvailable: doc.CheckoutAvailable,
		UpdatedAt:         doc.UpdatedAt.UTC(),
	}
	for _, c := range doc.Categories {
		out.Categories = append(out.Categories, category(c))
	}

	return out
}

func fromDocument(doc document, version int64) ports.OfferDocument {
	out := ports.OfferDocument{
		CategoryID:        doc.CategoryID,
		CategoryPath:      doc.CategoryPath,
		Attributes:        doc.Attributes,
		ID:                doc.OfferID,
		TenantID:          doc.TenantID,
		ProductID:         doc.ProductID,
		SellerSlug:        doc.SellerSlug,
		SellerName:        doc.SellerName,
		Delivery:          doc.Delivery.Clone(),
		FreeDeliveryFrom:  doc.FreeDeliveryFrom,
		Name:              doc.Name,
		Description:       doc.Description,
		Image:             doc.Image,
		URL:               doc.URL,
		SKU:               doc.SKU,
		Tags:              doc.Tags,
		VariantsID:        doc.VariantsID,
		Price:             doc.Price,
		BasePrice:         doc.BasePrice,
		Available:         doc.Available,
		InStock:           doc.InStock,
		Visible:           doc.Visible,
		Listed:            doc.Listed,
		CheckoutAvailable: doc.CheckoutAvailable,
		UpdatedAt:         doc.UpdatedAt,
		Version:           version,
	}
	for _, profile := range doc.PriceProfiles {
		out.PriceProfiles = append(out.PriceProfiles, catalog.PriceProfile{
			Key:            profile.Key,
			Price:          money.FromKopecks(profile.Price),
			EffectivePrice: money.FromKopecks(profile.EffectivePrice),
		})
	}
	for _, c := range doc.Categories {
		out.Categories = append(out.Categories, catalog.Category(c))
	}

	return out
}
