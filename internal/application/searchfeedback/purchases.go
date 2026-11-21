package searchfeedback

import (
	"context"
	"encoding/json"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/pkg/errs"
)

// Контракт задачи и бюджет одной попытки доставки покупки.
const (
	TaskPurchase        = "search.purchase"
	PurchaseVersion     = 1
	purchaseLifetime    = 2 * time.Hour
	PurchaseCallTimeout = time.Second
)

var errInvalidPurchase = errs.New(errs.Invalid, "search.invalid_purchase", "некорректное событие оплаты")

// Purchases доставляет сохранённые события, пока поиск может хранить квитанцию для повтора.
type Purchases struct {
	sink  ports.SearchPurchaseSink
	clock ports.Clock
}

// NewPurchases подключает доставку к общему outbox.
func NewPurchases(sink ports.SearchPurchaseSink, clock ports.Clock) *Purchases {
	return &Purchases{sink: sink, clock: clock}
}

// Deliver использует неизменный снимок из outbox на каждой попытке.
func (p *Purchases) Deliver(ctx context.Context, task ports.Task) error {
	var purchase ports.SearchPurchase
	if err := json.Unmarshal(task.Payload, &purchase); err != nil {
		return errInvalidPurchase.Wrap(err)
	}
	if purchase.Version != PurchaseVersion || purchase.EventID == "" || purchase.BuyerKey == "" ||
		purchase.PaidObservedAt.IsZero() || purchase.OrderCreatedAt.IsZero() || len(purchase.OfferIDs) == 0 {
		return errInvalidPurchase
	}
	if !p.clock.Now().Before(purchase.PaidObservedAt.Add(purchaseLifetime)) {
		return nil
	}
	return p.sink.Purchase(ctx, purchase)
}
