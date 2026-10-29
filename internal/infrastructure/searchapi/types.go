package searchapi

import (
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
)

// Offer хранит суммы в целых копейках. Категория и атрибуты — только системные
// коды uno; локальные идентификаторы справочников сюда не передаются.
type category struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	URL  string `json:"url"`
}

type priceProfile struct {
	Key            string `json:"key"`
	Price          int64  `json:"price"`
	EffectivePrice int64  `json:"effectivePrice"`
}

type offerDocument struct {
	PriceProfiles     []priceProfile               `json:"priceProfiles"`
	CategoryPath      []string                     `json:"categoryPath"`
	Delivery          participant.DeliverySettings `json:"delivery"`
	FreeDeliveryFrom  int64                        `json:"freeDeliveryFrom"`
	Categories        []category                   `json:"categories"`
	TenantID          string                       `json:"tenantId"`
	ProductID         string                       `json:"productId"`
	SellerSlug        string                       `json:"sellerSlug"`
	SellerName        string                       `json:"sellerName"`
	Name              string                       `json:"name"`
	Description       string                       `json:"description"`
	Image             string                       `json:"image"`
	URL               string                       `json:"url"`
	SKU               string                       `json:"sku"`
	CategoryID        string                       `json:"categoryId"`
	Attributes        []attribute                  `json:"attributes"`
	VariantsID        string                       `json:"variantsId"`
	Price             int64                        `json:"price"`
	BasePrice         int64                        `json:"basePrice"`
	Available         int                          `json:"available"`
	InStock           bool                         `json:"inStock"`
	Visible           bool                         `json:"visible"`
	Listed            bool                         `json:"listed"`
	CheckoutAvailable bool                         `json:"checkoutAvailable"`
	UpdatedAt         time.Time                    `json:"updatedAt"`
	Version           int64                        `json:"version"`
}

type attribute struct {
	Type         string   `json:"type,omitempty"`
	Key          string   `json:"key"`
	Value        string   `json:"value"`
	NumericValue *float64 `json:"numeric_value,omitempty"`
}

type offerQuery struct {
	PriceContexts map[string]string                `json:"priceContexts"`
	Attributes    map[string]ports.AttributeFilter `json:"attributes"`
	FacetFields   []facetField                     `json:"facetFields"`
	InStockSet    bool                             `json:"inStockSet"`
	Text          string                           `json:"text"`
	TenantID      string                           `json:"tenantId"`
	CategoryID    string                           `json:"categoryId"`
	PriceMin      *int64                           `json:"priceMin"`
	PriceMax      *int64                           `json:"priceMax"`
	InStock       bool                             `json:"inStock"`
	Sort          string                           `json:"sort"`
	Offset        int                              `json:"offset"`
	Limit         int                              `json:"limit"`
}

type offerPage struct {
	Facets []ports.Facet   `json:"facets"`
	Total  int             `json:"total"`
	Items  []offerDocument `json:"items"`
}

type offerBatch struct {
	Offers []offerDocument `json:"offers"`
}

type offerIdentity struct {
	TenantID  string `json:"tenantId"`
	ProductID string `json:"productId"`
}

type facetField struct {
	Code string `json:"code"`
	Name string `json:"name"`
	Type string `json:"type"`
	Unit string `json:"unit,omitempty"`
}
