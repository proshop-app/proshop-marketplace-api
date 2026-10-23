// Package buyer — покупатели маркетплейса: вход по звонку, сессии.
package buyer

import (
	"strings"
)

// Phone — номер в международном формате без плюса: только цифры, как его ждёт
// шлюз проверки звонком.
type Phone string

// ParsePhone приводит номер, набранный покупателем, к цифрам. Национальный
// префикс 8 у одиннадцатизначного номера заменяется на код страны 7: так
// номер набирают в зоне обслуживания шлюза.
func ParsePhone(raw string) (Phone, error) {
	var digits strings.Builder

	for _, r := range raw {
		switch {
		case r >= '0' && r <= '9':
			digits.WriteRune(r)
		case r == '+' || r == ' ' || r == '-' || r == '(' || r == ')':
		default:
			return "", ErrInvalidPhone
		}
	}

	value := digits.String()
	if len(value) == 11 && value[0] == '8' {
		value = "7" + value[1:]
	}

	if len(value) < 11 || len(value) > 15 || value[0] == '0' {
		return "", ErrInvalidPhone
	}

	return Phone(value), nil
}

// Masked — номер для журналов: видны код страны и две последние цифры.
func (p Phone) Masked() string {
	value := string(p)
	if len(value) < 4 {
		return "***"
	}

	return "+" + value[:1] + strings.Repeat("*", len(value)-3) + value[len(value)-2:]
}
