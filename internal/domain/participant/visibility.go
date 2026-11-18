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

// SellerOf сохраняет имя скрытого продавца в корзине и объясняет недоступность без нового запроса.
func SellerOf(p *Participant, tenant *Tenant) *Seller {
	if p == nil {
		return nil
	}
	result := &Seller{Slug: p.Slug(), Name: p.Name()}
	if !p.DeliveryValid() {
		result.UnavailableReason = "delivery_unavailable"
	} else if !VisibilityOf(p, tenant).CheckoutAvailable {
		result.UnavailableReason = "seller_unavailable"
	}
	return result
}
