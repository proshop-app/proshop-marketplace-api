package participant

import (
	"strings"

	"github.com/proshop-app/proshop-marketplace/pkg/errs"
)

// ErrInvalidDeliverySettings — обязательные настройки участия не по контракту.
var ErrInvalidDeliverySettings = errs.New(
	errs.Invalid,
	"participant.invalid_delivery_settings",
	"обязательны минимум заказа, субсидия продавца и регионы доставки",
)

// DeliverySettings — коды регионов перевозчиков и подключённые способы.
// Регион кодируется как <carrier>:<код региона в справочнике перевозчика>.
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
		if strings.TrimSpace(carrier) == "" {
			return ErrInvalidDeliverySettings
		}
	}
	for _, region := range d.Regions {
		carrier, code, ok := strings.Cut(region, ":")
		if !ok || code == "" || !contains(d.Carriers, carrier) {
			return ErrInvalidDeliverySettings
		}
	}
	return nil
}

// Allows запрещает доставку вне зон или без известного кода региона ПВЗ.
func (d DeliverySettings) Allows(carrier, region string) bool {
	return contains(d.Carriers, carrier) && region != "" && contains(d.Regions, carrier+":"+region)
}

// Clone отделяет списки настроек от изменяемого снимка вызывающего.
func (d DeliverySettings) Clone() DeliverySettings {
	return DeliverySettings{
		Regions:  append([]string(nil), d.Regions...),
		Carriers: append([]string(nil), d.Carriers...),
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
