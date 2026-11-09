package participant

import (
	"math"
	"regexp"
	"strconv"
	"strings"
)

const cardMinimumContrast = 4.5

var cardHexColor = regexp.MustCompile(`^#[0-9A-F]{6}$`)

// CardAppearance — независимое оформление основной части карточки и зоны логотипа.
type CardAppearance struct {
	Background     string `json:"background"`
	LogoBackground string `json:"logoBackground"`
	TextColor      string `json:"textColor"`
}

// DefaultCardAppearance не зависит от фирменного цвета витрины магазина.
func DefaultCardAppearance() CardAppearance {
	return CardAppearance{
		Background:     "#125F67",
		LogoBackground: "#E9EEE7",
		TextColor:      "white",
	}
}

// Normalize допускает отсутствие старого оформления, но не неполную или нечитаемую настройку.
func (a CardAppearance) Normalize() (CardAppearance, error) {
	if a == (CardAppearance{}) {
		return DefaultCardAppearance(), nil
	}
	a.Background = strings.ToUpper(strings.TrimSpace(a.Background))
	a.LogoBackground = strings.ToUpper(strings.TrimSpace(a.LogoBackground))
	if !cardHexColor.MatchString(a.Background) || !cardHexColor.MatchString(a.LogoBackground) ||
		(a.TextColor != "black" && a.TextColor != "white") {
		return CardAppearance{}, ErrInvalidCardAppearance
	}
	luminance := cardLuminance(a.Background)
	contrast := (luminance + 0.05) / 0.05
	if a.TextColor == "white" {
		contrast = 1.05 / (luminance + 0.05)
	}
	if contrast < cardMinimumContrast {
		return CardAppearance{}, ErrInvalidCardAppearance
	}
	return a, nil
}

func cardLuminance(hex string) float64 {
	var channels [3]float64
	for i := range channels {
		value, _ := strconv.ParseUint(hex[1+i*2:3+i*2], 16, 8)
		channel := float64(value) / 255
		if channel <= 0.04045 {
			channels[i] = channel / 12.92
		} else {
			channels[i] = math.Pow((channel+0.055)/1.055, 2.4)
		}
	}
	return 0.2126*channels[0] + 0.7152*channels[1] + 0.0722*channels[2]
}
