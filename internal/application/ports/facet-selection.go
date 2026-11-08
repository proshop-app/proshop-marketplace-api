package ports

import "slices"

// FacetCode — код выбора фасетов API; attributes разворачивается в поля таксономии.
type FacetCode string

const (
	// FacetCategory — категории площадки.
	FacetCategory FacetCode = "category"
	// FacetTenant — магазины.
	FacetTenant FacetCode = "tenant"
	// FacetInStock — наличие.
	FacetInStock FacetCode = "inStock"
	// FacetPrice — границы и гистограмма цены.
	FacetPrice FacetCode = "price"
	// FacetAttributes — атрибутные фасеты таксономии, не код общего поискового контракта.
	FacetAttributes FacetCode = "attributes"
)

// Valid проверяет код без обращения к таксономии атрибутов.
func (code FacetCode) Valid() bool {
	switch code {
	case FacetCategory, FacetTenant, FacetInStock, FacetPrice, FacetAttributes:
		return true
	default:
		return false
	}
}

// WantsFacet применяет один выбор и к движку, и к fake-индексу, и к видимому ответу.
func (query OfferQuery) WantsFacet(code FacetCode) bool {
	return query.Facets == nil || slices.Contains(*query.Facets, code)
}
