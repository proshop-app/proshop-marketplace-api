package participant

import (
	"regexp"
	"strings"
)

var cardHexColor = regexp.MustCompile(`^#[0-9A-F]{6}$`)

// CardAppearance — выбранный магазином цвет; оттенки для интерфейса вычисляет витрина.
type CardAppearance struct {
	Color string `json:"color"`
}

// DefaultCardAppearance не зависит от фирменного цвета витрины магазина.
func DefaultCardAppearance() CardAppearance {
	return CardAppearance{Color: "#125F67"}
}

// Normalize принимает любой полный HEX, включая светлые цвета.
func (a CardAppearance) Normalize() (CardAppearance, error) {
	if a == (CardAppearance{}) {
		return DefaultCardAppearance(), nil
	}
	a.Color = strings.ToUpper(strings.TrimSpace(a.Color))
	if !cardHexColor.MatchString(a.Color) {
		return CardAppearance{}, ErrInvalidCardAppearance
	}
	return a, nil
}
