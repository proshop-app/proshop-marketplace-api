package participant

import (
	"context"
	"errors"

	appcatalog "github.com/proshop-app/proshop-marketplace/internal/application/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
)

// RegistryEvent — событие platform.tenant.* с идентификатором для дедупа.
type RegistryEvent struct {
	EventID string
	Event   participant.RegistryEvent
}

// Registry ведёт зеркало реестра магазинов платформы.
type Registry struct {
	ids   ports.IDs
	uow   ports.UnitOfWork
	clock ports.Clock
}

// NewRegistry — приём событий реестра.
func NewRegistry(uow ports.UnitOfWork, clock ports.Clock, identities ports.IDs) *Registry {
	return &Registry{uow: uow, clock: clock, ids: identities}
}

// Apply применяет событие реестра. Если магазин участвует, слаг участника
// сверяется с реестром, а предложения магазина пересобираются: их видимость
// зависит от статуса магазина.
func (r *Registry) Apply(ctx context.Context, event RegistryEvent) error {
	now := r.clock.Now()

	return r.uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		fresh, err := tx.Inbox().MarkProcessed(ctx, event.EventID, now)
		if err != nil || !fresh {
			return err
		}

		tenant, err := tx.Registry().Get(ctx, event.Event.TenantID)
		changed := true

		switch {
		case errors.Is(err, participant.ErrTenantNotFound):
			tenant, err = participant.NewTenant(event.Event, now)
		case err == nil:
			changed, err = tenant.Apply(event.Event, now)
		}
		if err != nil {
			return err
		}

		if changed {
			if err := tx.Registry().Save(ctx, tenant); err != nil {
				return err
			}
		}

		p, err := tx.Participants().Get(ctx, tenant.ID())
		if errors.Is(err, participant.ErrNotFound) {
			return createCandidate(ctx, tx, tenant, r.ids, now)
		}
		if err != nil {
			return err
		}

		if !changed {
			return nil
		}
		if p.SyncSlug(tenant, now) {
			if err := tx.Participants().Update(ctx, p); err != nil {
				return err
			}
		}

		return tx.Outbox().Enqueue(ctx, appcatalog.SyncTenantTask(tenant.ID(), now))
	})
}
