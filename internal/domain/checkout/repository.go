package checkout

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

// Причины ошибок страницы доступны оператору; исходная ошибка остаётся в журнале.
const (
	ReadHeader = "checkout.unreadable_header"
	ReadGroup  = "checkout.unreadable_group"
	ReadCursor = "checkout.invalid_stuck_at"
	ReadRender = "checkout.unreadable_response"
)

// Filter — курсор, владелец и граница времени ожидания для панели оператора.
type Filter struct {
	// HasOrders скрывает из кабинета намерения без документов магазина.
	HasOrders    bool
	BuyerID      ids.UUID
	AfterID      ids.UUID
	AfterStuckAt *time.Time
	Limit        int
	StuckBefore  time.Time
	OrdersOnly   bool
}

// ListCursor продолжает страницу по времени попадания в «зависшие» и ID либо только по ID.
type ListCursor struct {
	ID      ids.UUID
	StuckAt *time.Time
}

// ListEntry сохраняет позицию даже для покупки, которую не удалось прочитать.
type ListEntry struct {
	Cursor   ListCursor
	Checkout *Checkout
	Failures []ReadFailure
}

// ReadFailure несёт причину разбора и принадлежность строки для диагностики и проверки доступа.
type ReadFailure struct {
	ID       ids.UUID
	BuyerID  ids.UUID
	TenantID ids.TenantID
	Cause    error
	Reason   string
}

// PickupHistoryPage отделяет здоровые пункты от ошибок исходных покупок.
type PickupHistoryPage struct {
	Points   []Selection
	Failures []ReadFailure
}

// ReconcileEntry — заказ магазина, статус которого площадка сверяет пачкой.
type ReconcileEntry struct {
	Failure    error
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
	List(context.Context, Filter) ([]ListEntry, error)
	// PickupHistory — пункты выдачи, куда покупателю создавались заказы: без повторов, свежие первыми.
	PickupHistory(ctx context.Context, buyerID ids.UUID, limit int) (PickupHistoryPage, error)
	// Get — покупка; нет — ErrNotFound.
	Get(ctx context.Context, id ids.UUID) (*Checkout, error)
	// GetForBuyer проверяет владельца до чтения агрегата; чужая покупка или её отсутствие — ErrNotFound.
	GetForBuyer(ctx context.Context, buyerID, id ids.UUID) (*Checkout, error)
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

// ValidStuckFilter принимает первую страницу либо полный ключ продолжения.
func (f Filter) ValidStuckFilter() bool {
	return (f.AfterID == "" && f.AfterStuckAt == nil) || (f.AfterID != "" && ValidStuckCursor(f.AfterStuckAt))
}

// ValidStuckCursor проверяет диапазон и точность ключа PostgreSQL.
func ValidStuckCursor(at *time.Time) bool {
	return at != nil && at.UTC().Year() >= 1 && at.UTC().Year() <= 9999 && at.Nanosecond()%1000 == 0
}

// ReadError объединяет причины покупки для одной записи журнала.
func (e ListEntry) ReadError() *ReadFailure {
	if len(e.Failures) == 0 {
		return nil
	}
	failure := e.Failures[0]
	if len(e.Failures) == 1 {
		return &failure
	}
	var causes []error
	seen := map[string]bool{}
	for _, item := range e.Failures {
		switch item.Reason {
		case ReadHeader, ReadCursor:
			if seen[item.Reason] {
				continue
			}
			seen[item.Reason] = true
		case ReadGroup:
			if item.TenantID != "" {
				causes = append(causes, fmt.Errorf("магазин %s: %w", item.TenantID, item.Cause))
				continue
			}
		}
		causes = append(causes, item.Cause)
	}
	failure.Cause = errors.Join(causes...)
	return &failure
}

func (e *ReadFailure) Error() string { return e.Cause.Error() }
func (e *ReadFailure) Unwrap() error { return e.Cause }
