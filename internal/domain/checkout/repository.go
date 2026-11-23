package checkout

import (
	"context"
	"time"

	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

// Filter — курсор по ID, владелец и окно ожидания для панели оператора.
type Filter struct {
	// HasOrders скрывает из кабинета намерения без документов магазина.
	HasOrders   bool
	BuyerID     ids.UUID
	AfterID     ids.UUID
	Limit       int
	StuckBefore time.Time
	OrdersOnly  bool
	WorkDueAt   time.Time
}

// ReconcileEntry — заказ магазина, статус которого площадка сверяет пачкой.
type ReconcileEntry struct {
	CheckoutID ids.UUID
	TenantID   ids.TenantID
	OrderID    string
	Status     GroupStatus
	Revision   int64
	// Unreachable — группа показана покупателю как «магазин недоступен»: её возвращает любой ответ сверки.
	Unreachable bool
}

// ReconcileCursor — место продолжения обхода сверки: магазин и покупка последней строки.
type ReconcileCursor struct {
	TenantID   ids.TenantID
	CheckoutID ids.UUID
}

// Repository — покупки.
type Repository interface {
	List(context.Context, Filter) ([]*Checkout, error)
	// PickupHistory — пункты выдачи, куда покупателю создавались заказы: без повторов, свежие первыми.
	PickupHistory(ctx context.Context, buyerID ids.UUID, limit int) ([]Selection, error)
	// Get — покупка; нет — ErrNotFound.
	Get(ctx context.Context, id ids.UUID) (*Checkout, error)
	// ByKey — покупка покупателя по ключу идемпотентности; нет — ErrNotFound.
	ByKey(ctx context.Context, buyerID ids.UUID, key string) (*Checkout, error)
	// Add сохраняет новую покупку; ключ занят — ErrDuplicateKey.
	Add(ctx context.Context, checkout *Checkout) error
	// Update сохраняет условно по исходным статусу и версии; не совпали — ErrConflict.
	Update(ctx context.Context, checkout *Checkout) error
	// ReconcileDue — страница заказов суточной сверки в порядке магазина и покупки, без снимков покупок.
	ReconcileDue(ctx context.Context, after ReconcileCursor, now time.Time, limit int) ([]ReconcileEntry, error)
	// ExpireReconciliation снимает со сверки заказы, срок которой истёк.
	ExpireReconciliation(ctx context.Context, now time.Time) error
}
