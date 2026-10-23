package delivery

import (
	"time"

	"github.com/proshop-app/proshop-marketplace/pkg/money"
)

// QuoteStatus — можно ли доставить заказ продавца в выбранный пункт.
type QuoteStatus string

const (
	// QuoteAvailable — продавец везёт этим перевозчиком в этот пункт.
	QuoteAvailable QuoteStatus = "available"
	// QuoteUnavailable — не везёт; причина — в Reason.
	QuoteUnavailable QuoteStatus = "unavailable"
)

// Причины недоступности, которые маркетплейс ставит сам. Остальные приходят от магазина.
const (
	// ReasonSellerUnreachable — магазин не ответил на расчёт.
	ReasonSellerUnreachable = "seller_unreachable"
)

// Quote — расчёт доставки заказа одного продавца до пункта выдачи. Cost —
// стоимость по договору продавца, Subsidy — сколько из неё оплачивает
// продавец, BuyerCost — доля покупателя.
type Quote struct {
	Status    QuoteStatus
	Reason    string
	Cost      money.Money
	Subsidy   money.Money
	BuyerCost money.Money
	MinDays   int
	MaxDays   int
	ExpiresAt time.Time
}

// Offered — ответ магазина на расчёт.
type Offered struct {
	Available bool
	Reason    string
	Cost      money.Money
	MinDays   int
	MaxDays   int
	// ExpiresAt — до какого времени магазин держит цену; ноль — не сказал.
	ExpiresAt time.Time
}

// NewQuote собирает расчёт: субсидию продавца вычитает из стоимости, срок
// цены — не позже maxAge от расчёта, даже если магазин обещал дольше.
func NewQuote(offered Offered, subsidy func(money.Money) (money.Money, error), now time.Time, maxAge time.Duration) (Quote, error) {
	expires := now.Add(maxAge)
	if !offered.ExpiresAt.IsZero() && offered.ExpiresAt.Before(expires) {
		expires = offered.ExpiresAt
	}

	if !offered.Available {
		reason := offered.Reason
		if reason == "" {
			reason = "unavailable"
		}

		return Quote{Status: QuoteUnavailable, Reason: reason, ExpiresAt: expires}, nil
	}

	if offered.Cost.IsNegative() || offered.MinDays < 0 || offered.MaxDays < offered.MinDays {
		return Quote{}, ErrInvalidQuote
	}
	if !expires.After(now) {
		return Quote{}, ErrInvalidQuote.WithMessage("магазин вернул уже истёкший расчёт")
	}

	paid, err := subsidy(offered.Cost)
	if err != nil {
		return Quote{}, err
	}

	buyerCost, err := offered.Cost.Sub(paid)
	if err != nil {
		return Quote{}, err
	}

	return Quote{
		Status: QuoteAvailable, Cost: offered.Cost, Subsidy: paid, BuyerCost: buyerCost,
		MinDays: offered.MinDays, MaxDays: offered.MaxDays, ExpiresAt: expires,
	}, nil
}

// Unreachable — расчёт продавца, который не ответил.
func Unreachable(now time.Time) Quote {
	return Quote{Status: QuoteUnavailable, Reason: ReasonSellerUnreachable, ExpiresAt: now}
}

// Expired — цена расчёта больше не действует.
func (q Quote) Expired(now time.Time) bool {
	return !now.Before(q.ExpiresAt)
}
