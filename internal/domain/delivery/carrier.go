// Package delivery — перевозчики, справочники пунктов выдачи и расчёты
// доставки. Договоры и расчёт — у магазинов; справочники пунктов маркетплейс
// держит сам.
package delivery

import "regexp"

var carrierPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{1,31}$`)

// Carrier — код перевозчика, как его называет сервер установки ("cdek", "yandex").
type Carrier string

// CarrierInfo — подключённый перевозчик для переключателя на карте.
type CarrierInfo struct {
	Code Carrier
	Name string
}

// ParseCarrier проверяет код перевозчика.
func ParseCarrier(value string) (Carrier, error) {
	if !carrierPattern.MatchString(value) {
		return "", ErrInvalidCarrier
	}

	return Carrier(value), nil
}
