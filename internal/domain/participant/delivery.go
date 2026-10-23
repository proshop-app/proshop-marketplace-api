package participant

import (
	"github.com/proshop-app/proshop-marketplace/pkg/errs"
	"github.com/proshop-app/proshop-marketplace/pkg/regions"
)

// ErrInvalidDeliverySettings — обязательные настройки участия не по контракту.
var ErrInvalidDeliverySettings = errs.New(
	errs.Invalid,
	"participant.invalid_delivery_settings",
	"обязательны минимум заказа, субсидия продавца и регионы доставки",
)

// DeliverySettings — регионы ISO 3166-2 RU и подключённые перевозчики.
type DeliverySettings struct {
	Regions  []string `json:"regions"`
	Carriers []string `json:"carriers"`
}

// Validate проверяет форму списка зон и способов доставки.
func (d DeliverySettings) Validate() error {
	if len(d.Regions) == 0 || len(d.Carriers) == 0 {
		return ErrInvalidDeliverySettings
	}
	for _, carrier := range d.Carriers {
		if carrier != "cdek" && carrier != "yandex" {
			return ErrInvalidDeliverySettings
		}
	}
	for _, region := range d.Regions {
		if !regions.ValidRU(region) {
			return ErrInvalidDeliverySettings
		}
	}
	return nil
}

// Allows запрещает доставку вне зон или без известного кода региона ПВЗ.
func (d DeliverySettings) Allows(carrier, region string) bool {
	return (carrier == "cdek" || carrier == "yandex") &&
		contains(d.Carriers, carrier) && regions.ValidRU(region) && contains(d.Regions, region)
}

// Clone отделяет списки настроек от изменяемого снимка вызывающего.
func (d DeliverySettings) Clone() DeliverySettings {
	return DeliverySettings{
		Regions:  append([]string{}, d.Regions...),
		Carriers: append([]string{}, d.Carriers...),
	}
}

func contains(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}
