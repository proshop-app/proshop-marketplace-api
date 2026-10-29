package catalog

import (
	"strings"

	"github.com/proshop-app/proshop-marketplace/internal/domain/reference"
)

// ParseGTIN проверяет исходные цифры и контрольную сумму, не исправляя ошибочный ввод.
func ParseGTIN(value string) (string, bool) {
	switch len(value) {
	case 8, 12, 13, 14:
	default:
		return "", false
	}
	for _, digit := range value {
		if digit < '0' || digit > '9' {
			return "", false
		}
	}
	sum, weight := 0, 3
	for i := len(value) - 2; i >= 0; i-- {
		sum += int(value[i]-'0') * weight
		weight = 4 - weight
	}
	if (10-sum%10)%10 != int(value[len(value)-1]-'0') {
		return "", false
	}
	if len(value) < 13 {
		value = strings.Repeat("0", 13-len(value)) + value
	}

	return value, true
}

// ModelPartKey нормализует артикул производителя для сравнения.
func ModelPartKey(value string) string {
	return strings.NewReplacer(" ", "", "-", "").Replace(reference.Key(value))
}

// SetModelKey обновляет только результат наблюдения; исходные атрибуты неизменны.
func (p *Product) SetModelKey(key, source string) bool {
	if p.state.Card.ModelKey == key && p.state.Card.ModelKeySource == source {
		return false
	}
	p.state.Card.ModelKey = key
	p.state.Card.ModelKeySource = source
	p.state.Version = p.originVersion + 1

	return true
}
