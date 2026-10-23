package participant

// Visibility — как магазин виден покупателю маркетплейса.
type Visibility struct {
	// Visible — предложения магазина показываются в каталоге.
	Visible bool
	// CheckoutAvailable — предложения можно оформить на кассе магазина.
	CheckoutAvailable bool
}

// VisibilityOf требует согласия магазина и обязательных настроек доставки.
func VisibilityOf(p *Participant, tenant *Tenant) Visibility {
	if p == nil || tenant == nil || tenant.ID() != p.TenantID() {
		return Visibility{}
	}

	state := p.State()
	visible := p.Enabled() && p.ShopEnabled() && tenant.Status() == TenantActive &&
		state.Delivery.Validate() == nil && state.Subsidy.Validate() == nil && state.Subsidy.Enabled &&
		!state.FreeDeliveryFrom.IsNegative()

	return Visibility{Visible: visible, CheckoutAvailable: visible}
}
