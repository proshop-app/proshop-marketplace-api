package http

import (
	"encoding/json"
	"math/big"
	"strings"

	"github.com/proshop-app/proshop-marketplace/pkg/money"
)

// Публичная граница сохраняет различимость копейки в браузерном Number.
const maxPublicKopecks int64 = 999_999_999_999_999

// rubles хранит копейки на границе HTTP и пишет точное JSON-число рублей.
// Доменные Money и служебные контракты сохраняют прежнюю сериализацию копеек.
type rubles int64

func (value rubles) MarshalJSON() ([]byte, error) {
	if value > rubles(maxPublicKopecks) || value < -rubles(maxPublicKopecks) {
		return nil, money.ErrOverflow
	}
	return []byte(money.FromKopecks(int64(value)).String()), nil
}

func (value *rubles) UnmarshalJSON(raw []byte) error {
	parsed, err := parseRubles(string(raw))
	if err != nil {
		return err
	}
	*value = rubles(parsed.Kopecks())
	return nil
}

func parseRubles(raw string) (money.Money, error) {
	if !json.Valid([]byte(raw)) || raw == "null" || strings.ContainsAny(raw, "eE+\"") {
		return money.Money{}, money.ErrInvalid
	}
	if _, fraction, point := strings.Cut(raw, "."); point && len(fraction) > 2 {
		return money.Money{}, money.ErrInvalid.WithMessage("сумма в рублях — не больше двух знаков после точки")
	}
	value, err := money.ParseRubles(raw)
	if err != nil {
		return money.Money{}, err
	}
	if value.Kopecks() > maxPublicKopecks || value.Kopecks() < -maxPublicKopecks {
		return money.Money{}, money.ErrOverflow
	}
	return value, nil
}

func rublesPointer(value *int64) *rubles {
	if value == nil {
		return nil
	}
	result := rubles(*value)
	return &result
}

// lineBreakdown использует только согласованный снимок магазина, без цен каталога.
func lineBreakdown(price money.Money, quantity int, amount money.Money) (money.Money, money.Money, int, error) {
	base, err := price.MulRatio(int64(quantity), 1)
	if err != nil {
		return money.Money{}, money.Money{}, 0, err
	}
	discount, err := base.Sub(amount)
	if err != nil || discount.IsNegative() || amount.IsNegative() {
		return money.Money{}, money.Money{}, 0, money.ErrInvalid
	}
	percent := 0
	if !base.IsZero() {
		numerator := new(big.Int).Mul(big.NewInt(discount.Kopecks()), big.NewInt(200))
		numerator.Add(numerator, big.NewInt(base.Kopecks()))
		denominator := new(big.Int).Mul(big.NewInt(base.Kopecks()), big.NewInt(2))
		percent = int(numerator.Quo(numerator, denominator).Int64())
	}
	return base, discount, percent, nil
}
