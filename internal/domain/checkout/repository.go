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

// WorkCursor — место продолжения прохода восстановления: срок работы и покупка последней группы.
type WorkCursor struct {
	At         time.Time
	CheckoutID ids.UUID
}

// BrokenCheckout — покупка страницы восстановления, которую не удалось разобрать.
type BrokenCheckout struct {
	ID  ids.UUID
	Err error
}

// WorkPage — страница прохода восстановления. Покупки, занятые другой записью, в неё не входят;
// неразобравшиеся строки идут в Broken, а не роняют страницу.
type WorkPage struct {
	Due    []*Checkout
	Broken []BrokenCheckout
	// Deferred — взятые покупки с отложенной ранее работой: проход сохраняет их, даже если ничего
	// не изменилось, чтобы снять признак.
	Deferred []ids.UUID
	// Next — курсор по последней просмотренной группе; More — страница групп была полной.
	Next WorkCursor
	More bool
}

// Repository — покупки.
type Repository interface {
	List(context.Context, Filter) ([]*Checkout, error)
	// PickupHistory — пункты выдачи, куда покупателю создавались заказы: без повторов, свежие первыми.
	PickupHistory(ctx context.Context, buyerID ids.UUID, limit int) ([]Selection, error)
	// Get — покупка; нет — ErrNotFound.
	Get(ctx context.Context, id ids.UUID) (*Checkout, error)
	// GetForUpdate — покупка под запись в транзакции: параллельная запись той же покупки ждёт её конца,
	// а не проигрывает условное обновление версии; нет — ErrNotFound.
	GetForUpdate(ctx context.Context, id ids.UUID) (*Checkout, error)
	// ByKey — покупка покупателя по ключу идемпотентности; нет — ErrNotFound.
	ByKey(ctx context.Context, buyerID ids.UUID, key string) (*Checkout, error)
	// Add сохраняет новую покупку; ключ занят — ErrDuplicateKey.
	Add(ctx context.Context, checkout *Checkout) error
	// Update сохраняет условно по исходным статусу и версии; не совпали — ErrConflict.
	Update(ctx context.Context, checkout *Checkout) error
	// UpdateMany сохраняет пачку покупок, как Update каждую; хоть одна не совпала — ErrConflict.
	UpdateMany(ctx context.Context, checkouts []*Checkout) error
	// LockWorkDue — покупки limit групп с созревшей работой после курсора в порядке срока работы,
	// под блокировкой до конца транзакции; занятые покупки пропускаются.
	LockWorkDue(ctx context.Context, now time.Time, after WorkCursor, limit int) (WorkPage, error)
	// DeferWork переносит созревшую работу групп покупок на until: неразбирающаяся покупка не
	// занимает каждый проход, но и не теряется.
	// now — момент, с которого работа отложена: признак виден оператору до удачного сохранения.
	DeferWork(ctx context.Context, checkoutIDs []ids.UUID, until, now time.Time) error
	// ReconcileDue — страница заказов суточной сверки в порядке магазина и покупки, без снимков покупок.
	ReconcileDue(ctx context.Context, after ReconcileCursor, now time.Time, limit int) ([]ReconcileEntry, error)
	// ExpireReconciliation снимает со сверки заказы, срок которой истёк.
	ExpireReconciliation(ctx context.Context, now time.Time) error
}
