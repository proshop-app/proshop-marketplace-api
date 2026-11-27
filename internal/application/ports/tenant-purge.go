package ports

import (
	"context"
	"time"

	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

// TaskPurgeTenant продолжает очистку одного магазина.
const TaskPurgeTenant = "participant.tenant.purge"

// TenantPurgeCursor хранит следующий шаг и позицию обхода предложений.
type TenantPurgeCursor struct {
	Step      int64
	Product   ids.ProductID
	IndexDone bool
	Complete  bool
}

// TenantPurge — отметка удаления и порционная очистка собственных данных магазина.
type TenantPurge interface {
	// Available удерживает общую блокировку до конца записи входящего события.
	Available(context.Context, []ids.TenantID) (map[ids.TenantID]bool, error)
	Begin(context.Context, ids.TenantID) (bool, error)
	LockCursor(context.Context, ids.TenantID) (TenantPurgeCursor, error)
	IndexPage(context.Context, ids.TenantID, int64, int) ([]ids.ProductID, bool, error)
	// DeletePage возвращает время продолжения. Нулевое время означает пустой остаток.
	DeletePage(context.Context, ids.TenantID, int) (time.Time, error)
	Finish(context.Context, ids.TenantID) error
	Advance(context.Context, ids.TenantID, TenantPurgeCursor) error
}
