package participant

// Visibility — как магазин виден покупателю маркетплейса.
type Visibility struct {
	// Visible — предложения магазина показываются в каталоге.
	Visible bool
	// CheckoutAvailable — предложения можно оформить на кассе магазина.
	CheckoutAvailable bool
}

// VisibilityOf требует подключения оператором и валидных настроек доставки.
func VisibilityOf(p *Participant, tenant *Tenant) Visibility {
	if p == nil || tenant == nil || tenant.ID() != p.TenantID() {
		return Visibility{}
	}

	visible := p.Enabled() && tenant.Status() == TenantActive && p.DeliveryValid()

	return Visibility{Visible: visible, CheckoutAvailable: visible}
}
