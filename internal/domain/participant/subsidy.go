package participant

import (
	"github.com/proshop-app/proshop-marketplace/pkg/errs"
	"github.com/proshop-app/proshop-marketplace/pkg/money"
)

// ErrInvalidSubsidy — настройки субсидии с сервера не проходят проверку.
var ErrInvalidSubsidy = errs.New(errs.Invalid, "participant.invalid_subsidy", "некорректные настройки субсидии доставки")

// SubsidyType — как продавец оплачивает часть доставки покупателя.
type SubsidyType string

const (
	// SubsidyFixed — фиксированная сумма в копейках.
	SubsidyFixed SubsidyType = "fixed"
	// SubsidyPercent — процент стоимости доставки, целое от 0 до 100.
	SubsidyPercent SubsidyType = "percent"
)

// Subsidy — субсидия доставки продавца из настроек магазина.
type Subsidy struct {
	Enabled bool
	Type    SubsidyType
	// Value — копейки для fixed, процент для percent.
	Value int64
	// MaxAmount — потолок субсидии; ноль — без потолка.
	MaxAmount money.Money
}

// Validate проверяет настройки.
func (s Subsidy) Validate() error {
	if !s.Enabled {
		return nil
	}

	switch {
	case s.Type == SubsidyFixed && s.Value >= 0:
	case s.Type == SubsidyPercent && s.Value >= 0 && s.Value <= 100:
	default:
		return ErrInvalidSubsidy
	}

	if s.MaxAmount.IsNegative() {
		return ErrInvalidSubsidy
	}

	return nil
}

// Apply — сколько доставки оплачивает продавец: не больше стоимости и потолка.
func (s Subsidy) Apply(cost money.Money) (money.Money, error) {
	if !s.Enabled || cost.IsNegative() || cost.IsZero() {
		return money.Money{}, nil
	}

	var (
		amount money.Money
		err    error
	)

	switch s.Type {
	case SubsidyFixed:
		amount = money.FromKopecks(s.Value)
	case SubsidyPercent:
		amount, err = cost.MulRatio(s.Value, 100)
		if err != nil {
			return money.Money{}, err
		}
	default:
		return money.Money{}, ErrInvalidSubsidy
	}

	if !s.MaxAmount.IsZero() {
		amount = money.Min(amount, s.MaxAmount)
	}

	return money.Min(amount, cost), nil
}
