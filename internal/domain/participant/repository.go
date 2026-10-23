package participant

import (
	"context"

	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

// Page — окно списка.
type Page struct {
	Limit  int
	Offset int
}

// Repository — хранилище участников.
type Repository interface {
	// Get — участник; нет — ErrNotFound.
	Get(ctx context.Context, tenantID ids.TenantID) (*Participant, error)
	// List — участники по времени создания и общее число.
	List(ctx context.Context, page Page) ([]*Participant, int, error)
	// Add сохраняет нового участника; занят магазин — ErrAlreadyExists, слаг — ErrSlugTaken.
	Add(ctx context.Context, p *Participant) error
	// Update сохраняет изменения условно по исходным статусу и версии; не совпали — ErrConflict.
	Update(ctx context.Context, p *Participant) error
}

// Registry — зеркало реестра магазинов платформы.
type Registry interface {
	// Get — запись; нет — ErrTenantNotFound.
	Get(ctx context.Context, tenantID ids.TenantID) (*Tenant, error)
	// Save заводит новую запись или обновляет условно по исходной версии; не совпала — ErrTenantConflict.
	Save(ctx context.Context, tenant *Tenant) error
}
