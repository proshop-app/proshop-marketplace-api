package participant

// Visibility — как магазин виден покупателю маркетплейса.
type Visibility struct {
	// Visible — предложения магазина показываются в каталоге.
	Visible bool
	// CheckoutAvailable — предложения можно оформить: магазин продавца у провайдера привязан.
	CheckoutAvailable bool
}

// VisibilityOf — участник виден, если оператор его включил и магазин работает
// в реестре платформы; оформить заказ можно только у подключённого к оплате.
func VisibilityOf(p *Participant, tenant *Tenant) Visibility {
	if p == nil || tenant == nil || tenant.ID() != p.TenantID() {
		return Visibility{}
	}

	visible := p.Enabled() && tenant.Status() == TenantActive

	return Visibility{Visible: visible, CheckoutAvailable: visible && p.Status() == StatusConnected}
}
