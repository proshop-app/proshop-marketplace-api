package participant

// Visibility — как магазин виден покупателю маркетплейса.
type Visibility struct {
	// Visible — предложения магазина показываются в каталоге.
	Visible bool
	// CheckoutAvailable — по предложениям можно оформить заказ.
	CheckoutAvailable bool
}

// VisibilityOf требует подключение, активный магазин и валидную доставку.
func VisibilityOf(p *Participant, tenant *Tenant) Visibility {
	if p == nil || tenant == nil || tenant.ID() != p.TenantID() {
		return Visibility{}
	}

	visible := p.Enabled() && tenant.Status() == TenantActive && p.DeliveryValid()

	return Visibility{Visible: visible, CheckoutAvailable: visible}
}
