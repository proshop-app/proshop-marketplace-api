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

// ComposeOffer собирает предложение из снимка карточки, сигналов склада и
// цены и видимости продавца.
//
// Сигнал свежее снимка побеждает: остаток и прайс меняются чаще карточки.
// Новый прайс отменяет промо-цену снимка: она посчитана от старого прайса, и
// показывать её до следующего снимка — значит обещать цену, которой у
// магазина уже может не быть.
func ComposeOffer(product *Product, signals *Signals, seller Seller) Offer {
	if product == nil {
		return Offer{}
	}

	state := product.State()
	offer := Offer{ID: ids.OfferID{Tenant: state.TenantID, Product: state.ProductID}, Seller: seller}

	if state.Deleted {
		return offer
	}

	base := state.Price
	promo := state.PromoPrice
	available := state.Available

	if signals != nil {
		sig := signals.State()
		if sig.HasPrice && sig.PriceAt.After(state.ProjectedAt) {
			base = sig.Price
			promo = nil
		}
		if sig.HasStock && sig.StockAt.After(state.ProjectedAt) {
			available = sig.Available
		}
	}

	price := base
	if promo != nil && !promo.IsZero() && promo.Cmp(base) < 0 {
		price = *promo
	}

	offer.Card = state.Card
	offer.Price = price
	offer.BasePrice = base
	offer.Available = available
	offer.InStock = available > 0
	offer.Visible = seller.Visible && state.Card.Visible
	offer.Listed = offer.Visible && !state.Card.OnlyVariant
	offer.CheckoutAvailable = offer.Visible && seller.CheckoutAvailable

	return offer
}
