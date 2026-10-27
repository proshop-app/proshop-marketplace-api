package catalog

import (
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
	"github.com/proshop-app/proshop-marketplace/pkg/money"
)

// Seller — как магазин-участник виден покупателю.
type Seller struct {
	Slug              string
	Name              string
	Visible           bool
	CheckoutAvailable bool
}

// Offer — предложение маркетплейса: товар магазина с ценой и наличием.
type Offer struct {
	ID     ids.OfferID
	Seller Seller
	Card   Card
	// Price — цена продажи: промо-цена, если она ниже прайса, иначе прайс.
	Price money.Money
	// BasePrice — прайс магазина.
	BasePrice money.Money
	Available int
	InStock   bool
	// Visible — предложение можно открыть.
	Visible bool
	// Listed — предложение показывается в выдаче каталога.
	Listed bool
	// CheckoutAvailable — предложение можно оформить через маркетплейс.
	CheckoutAvailable bool
}

// ComposeOffer берёт цену из проекции Pricing и остаток из последнего складского события.
func ComposeOffer(product *Product, signals *Signals, seller Seller) Offer {
	if product == nil {
		return Offer{}
	}
	state := product.State()
	offer := Offer{ID: ids.OfferID{Tenant: state.TenantID, Product: state.ProductID}, Seller: seller}

	if state.Deleted {
		return offer
	}

	profile, found := SelectPrice(state.PriceProfiles, DefaultPriceProfile)
	if !found {
		return offer
	}
	base := profile.Price
	price := profile.EffectivePrice
	available := state.Available

	if signals != nil {
		sig := signals.State()
		if sig.HasStock && sig.StockAt.After(state.ProjectedAt) {
			available = sig.Available
		}
	}

	offer.Card = state.Card
	offer.Price = price
	offer.BasePrice = base
	offer.Available = available
	offer.InStock = available > 0
	offer.Visible = seller.Visible && state.Card.Visible && state.Card.Publication.Published
	offer.Listed = offer.Visible && !state.Card.OnlyVariant
	offer.CheckoutAvailable = offer.Visible && seller.CheckoutAvailable

	return offer
}
