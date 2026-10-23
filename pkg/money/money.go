// Package money — деньги в целых копейках. Сложение и вычитание идут только
// через методы типа: они ловят переполнение, а дробных рублей в коде нет.
package money

import (
	"math"
	"math/big"
	"strconv"
	"strings"

	"github.com/proshop-app/proshop-marketplace/pkg/errs"
)

var (
	// ErrInvalid — строка не является суммой.
	ErrInvalid = errs.New(errs.Invalid, "money.invalid", "некорректная сумма")
	// ErrOverflow — результат не помещается в диапазон копеек.
	ErrOverflow = errs.New(errs.Invalid, "money.overflow", "сумма вне допустимого диапазона")
)

// Money — сумма в копейках. Нулевое значение — ноль рублей.
type Money struct {
	kopecks int64
}

// FromKopecks — сумма из целых копеек.
func FromKopecks(kopecks int64) Money {
	return Money{kopecks: kopecks}
}

// ParseRubles читает десятичную запись рублей ("199", "199.9", "199.99").
// Дробь длиннее копеек округляется до копейки половиной от нуля: так
// приходят цены, посчитанные процентом от прайса. Экспонента не принимается.
func ParseRubles(value string) (Money, error) {
	text := strings.TrimSpace(value)
	negative := false

	switch {
	case strings.HasPrefix(text, "-"):
		negative = true
		text = text[1:]
	case strings.HasPrefix(text, "+"):
		text = text[1:]
	}

	whole, fraction, hasPoint := strings.Cut(text, ".")
	if whole == "" || !digitsOnly(whole) || (hasPoint && (fraction == "" || !digitsOnly(fraction))) {
		return Money{}, ErrInvalid.WithMessage("некорректная сумма: " + value)
	}

	padded := fraction + "000"
	cents := int64(padded[0]-'0')*10 + int64(padded[1]-'0')

	if padded[2] >= '5' {
		cents++
	}

	rubles, err := strconv.ParseInt(whole, 10, 64)
	if err != nil || rubles > (math.MaxInt64-cents)/100 {
		return Money{}, ErrOverflow.WithMessage("сумма вне допустимого диапазона: " + value)
	}

	kopecks := rubles*100 + cents

	if negative {
		kopecks = -kopecks
	}

	return Money{kopecks: kopecks}, nil
}

func digitsOnly(text string) bool {
	for _, r := range text {
		if r < '0' || r > '9' {
			return false
		}
	}

	return true
}

// Kopecks — сумма в копейках.
func (m Money) Kopecks() int64 { return m.kopecks }

// Add — сумма двух значений.
func (m Money) Add(other Money) (Money, error) {
	if (other.kopecks > 0 && m.kopecks > math.MaxInt64-other.kopecks) ||
		(other.kopecks < 0 && m.kopecks < math.MinInt64-other.kopecks) {
		return Money{}, ErrOverflow
	}

	return Money{kopecks: m.kopecks + other.kopecks}, nil
}

// Sub — разность двух значений.
func (m Money) Sub(other Money) (Money, error) {
	if (other.kopecks < 0 && m.kopecks > math.MaxInt64+other.kopecks) ||
		(other.kopecks > 0 && m.kopecks < math.MinInt64+other.kopecks) {
		return Money{}, ErrOverflow
	}

	return Money{kopecks: m.kopecks - other.kopecks}, nil
}

// MulRatio — доля суммы num/den с округлением к нулю: процент субсидии,
// доля доставки. Знаменатель — больше нуля.
func (m Money) MulRatio(num, den int64) (Money, error) {
	if den <= 0 {
		return Money{}, ErrInvalid.WithMessage("знаменатель доли должен быть больше нуля")
	}

	product := new(big.Int).Mul(big.NewInt(m.kopecks), big.NewInt(num))
	product.Quo(product, big.NewInt(den))

	if !product.IsInt64() {
		return Money{}, ErrOverflow
	}

	return Money{kopecks: product.Int64()}, nil
}

// Min — меньшая из сумм.
func Min(a, b Money) Money {
	if a.Cmp(b) <= 0 {
		return a
	}

	return b
}

// Cmp сравнивает суммы: -1, если m меньше, 0 — равны, 1 — больше.
func (m Money) Cmp(other Money) int {
	switch {
	case m.kopecks < other.kopecks:
		return -1
	case m.kopecks > other.kopecks:
		return 1
	default:
		return 0
	}
}

// IsZero — ноль копеек.
func (m Money) IsZero() bool { return m.kopecks == 0 }

// IsNegative — сумма меньше нуля.
func (m Money) IsNegative() bool { return m.kopecks < 0 }

// String — рубли с копейками через точку: "1234.50".
func (m Money) String() string {
	sign := ""
	kopecks := m.kopecks

	if kopecks < 0 {
		sign = "-"
	}

	rubles := kopecks / 100
	rest := kopecks % 100

	if rubles < 0 {
		rubles = -rubles
	}
	if rest < 0 {
		rest = -rest
	}

	return sign + strconv.FormatInt(rubles, 10) + "." + leftPad(strconv.FormatInt(rest, 10))
}

func leftPad(digits string) string {
	if len(digits) == 1 {
		return "0" + digits
	}

	return digits
}
