package delivery

// Carriers — реализованные службы договора площадки; все обязательны для магазина.
func Carriers() []CarrierInfo {
	return []CarrierInfo{
		{Code: "cdek", Name: "СДЭК"},
		{Code: "yandex", Name: "Яндекс Доставка"},
	}
}
