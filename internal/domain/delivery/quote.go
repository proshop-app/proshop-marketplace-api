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

// Причины недоступности доставки — закрытый словарь docs/server-contract.md,
// расширяется только через него. Первые три присылает магазин,
// seller_unreachable маркетплейс ставит сам.
const (
	// ReasonCarrierNotConnected — у магазина нет этого перевозчика.
	ReasonCarrierNotConnected = "carrier_not_connected"
	// ReasonPickupPointUnavailable — перевозчик не везёт в этот пункт.
	ReasonPickupPointUnavailable = "pickup_point_unavailable"
	// ReasonNoRate — тариф не рассчитан: вес, габариты или зона.
	ReasonNoRate = "no_rate"
	// ReasonSellerUnreachable — магазин не ответил на расчёт.
	ReasonSellerUnreachable = "seller_unreachable"
	ReasonSellerNotReady    = "seller_not_ready"
	// ReasonRegionNotAllowed — регион общего ПВЗ не входит в область доставки группы.
	ReasonRegionNotAllowed = "region_not_allowed"
)

// sellerReasons — причины, которые вправе прислать магазин.
var sellerReasons = map[string]bool{
	ReasonCarrierNotConnected:    true,
	ReasonPickupPointUnavailable: true,
	ReasonNoRate:                 true,
}

// Quote — расчёт доставки заказа одного продавца до пункта выдачи. Cost —
// стоимость перевозчика — расход продавца. Для покупателя доставка бесплатна.
type Quote struct {
	Status    QuoteStatus
	Reason    string
	Cost      money.Money
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

// NewQuote сохраняет тариф продавца; срок цены — не позже maxAge от расчёта, даже если магазин обещал дольше. Причина
// недоступности не из словаря — ответ не по контракту.
func NewQuote(
	offered Offered,
	now time.Time,
	maxAge time.Duration,
) (Quote, error) {
	expires := now.Add(maxAge)
	if !offered.ExpiresAt.IsZero() && offered.ExpiresAt.Before(expires) {
		expires = offered.ExpiresAt
	}

	if !offered.Available {
		if !sellerReasons[offered.Reason] {
			return Quote{}, ErrInvalidQuote.WithMessage("причина недоступности не из словаря: " + offered.Reason)
		}

		return Quote{Status: QuoteUnavailable, Reason: offered.Reason, ExpiresAt: expires}, nil
	}

	if offered.Cost.IsNegative() || offered.MinDays < 0 || offered.MaxDays < offered.MinDays {
		return Quote{}, ErrInvalidQuote
	}
	if !expires.After(now) {
		return Quote{}, ErrInvalidQuote.WithMessage("магазин вернул уже истёкший расчёт")
	}

	return Quote{
		Status: QuoteAvailable,
		Cost:   offered.Cost,

		MinDays:   offered.MinDays,
		MaxDays:   offered.MaxDays,
		ExpiresAt: expires,
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
