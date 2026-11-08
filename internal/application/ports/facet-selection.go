package ports

// FacetCode — базовый фасет общего поискового контракта 5.1.
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
)

// Valid проверяет код без обращения к таксономии атрибутов.
func (code FacetCode) Valid() bool {
	switch code {
	case FacetCategory, FacetTenant, FacetInStock, FacetPrice:
		return true
	default:
		return false
	}
}
