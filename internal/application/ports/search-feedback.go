package ports

import (
	"context"
	"time"
)

// SearchFeedback — органическое событие и проверенный псевдоним для клика.
type SearchFeedback struct {
	Kind     string
	Receipt  string
	OfferID  string
	OfferIDs []string
	BuyerKey string
}

// SearchPurchase — событие первого подтверждения оплаты заказа продавца.
type SearchPurchase struct {
	Version        int       `json:"version"`
	EventID        string    `json:"eventId"`
	BuyerKey       string    `json:"buyerKey"`
	OrderCreatedAt time.Time `json:"orderCreatedAt"`
	PaidObservedAt time.Time `json:"paidObservedAt"`
	OfferIDs       []string  `json:"offerIds"`
}

// SearchFeedbackSink принимает события органической выдачи.
type SearchFeedbackSink interface {
	Feedback(context.Context, SearchFeedback) (bool, error)
}

// SearchPurchaseSink подтверждает обработку события покупки.
type SearchPurchaseSink interface {
	Purchase(context.Context, SearchPurchase) error
}
