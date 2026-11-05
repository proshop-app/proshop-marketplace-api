// Package participant — сценарии участников маркетплейса и зеркала реестра
// магазинов платформы.
package participant

import (
	"context"
	"errors"

	appcatalog "github.com/proshop-app/proshop-marketplace/internal/application/catalog"
	appmoderation "github.com/proshop-app/proshop-marketplace/internal/application/moderation"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

// View — участник с состоянием магазина в реестре.
type View struct {
	Participant  participant.State
	TenantStatus participant.TenantStatus
	Visibility   participant.Visibility
}

// List — страница участников.
type List struct {
	Items []View
	Total int
}

// CreateCommand — завести участника.
type CreateCommand struct {
	TenantID ids.TenantID
	Name     string
}

// MaxPageLimit — наибольший размер страницы списка участников.
const MaxPageLimit = 200

// Service — операторские сценарии участников. Любое изменение участника
// ставит задачу синхронизации индекса в той же транзакции.
type Service struct {
	ids   ports.IDs
	uow   ports.UnitOfWork
	clock ports.Clock
}

// NewService — сценарии участников.
func NewService(uow ports.UnitOfWork, clock ports.Clock, identities ports.IDs) *Service {
	return &Service{uow: uow, clock: clock, ids: identities}
}

// Create заводит участника для магазина из реестра платформы.
func (s *Service) Create(ctx context.Context, cmd CreateCommand) (View, error) {
	now := s.clock.Now()

	var view View

	err := s.uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		tenant, err := tx.Registry().Get(ctx, cmd.TenantID)
		if errors.Is(err, participant.ErrTenantNotFound) {
			return participant.ErrTenantUnknown
		}
		if err != nil {
			return err
		}

		if _, err := tx.Participants().Get(ctx, cmd.TenantID); err == nil {
			return participant.ErrAlreadyExists
		} else if !errors.Is(err, participant.ErrNotFound) {
			return err
		}

		p, err := participant.New(tenant, cmd.Name, now)
		if err != nil {
			return err
		}
		if err := tx.Participants().Add(ctx, p); err != nil {
			return err
		}
		if err := tx.Outbox().Enqueue(ctx, appcatalog.SyncTenantTask(cmd.TenantID, now)); err != nil {
			return err
		}
		// Снимки товаров могли прийти раньше участника и остаться без вердикта:
		// переоценка даёт им допуск, иначе они скрыты до следующей ревизии.
		if err := tx.Outbox().Enqueue(ctx, appmoderation.ReevaluateTask(cmd.TenantID, now)); err != nil {
			return err
		}

		view = viewOf(p, tenant)

		return nil
	})

	return view, err
}

// Rename меняет название участника.
func (s *Service) Rename(ctx context.Context, tenantID ids.TenantID, name string) (View, error) {
	return s.change(ctx, tenantID, func(p *participant.Participant) (bool, error) {
		return p.Rename(name, s.clock.Now())
	})
}

// Enable включает участника в каталог.
func (s *Service) Enable(ctx context.Context, tenantID ids.TenantID, decision participant.Decision) (View, error) {
	return s.decide(ctx, tenantID, "enable", "", decision)
}

// Disable сохраняет решение вместе с отключением и задачей индекса.
func (s *Service) Disable(ctx context.Context, tenantID ids.TenantID, decision participant.Decision) (View, error) {
	return s.decide(ctx, tenantID, "disable", "", decision)
}

// Get — участник.
func (s *Service) Get(ctx context.Context, tenantID ids.TenantID) (View, error) {
	var view View

	err := s.uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		p, err := tx.Participants().Get(ctx, tenantID)
		if err != nil {
			return err
		}

		view, err = withTenant(ctx, tx, p)

		return err
	})

	return view, err
}

// List — участники по времени создания.
func (s *Service) List(ctx context.Context, page participant.Page) (List, error) {
	if page.Limit <= 0 || page.Limit > MaxPageLimit {
		page.Limit = MaxPageLimit
	}
	if page.Offset < 0 {
		page.Offset = 0
	}

	var list List

	err := s.uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		items, total, err := tx.Participants().List(ctx, page)
		if err != nil {
			return err
		}

		list = List{Items: make([]View, 0, len(items)), Total: total}
		for _, p := range items {
			view, err := withTenant(ctx, tx, p)
			if err != nil {
				return err
			}

			list.Items = append(list.Items, view)
		}

		return nil
	})

	return list, err
}

func (s *Service) change(
	ctx context.Context,
	tenantID ids.TenantID,
	mutate func(*participant.Participant) (bool, error),
) (View, error) {
	var view View

	err := s.uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		p, err := tx.Participants().Get(ctx, tenantID)
		if err != nil {
			return err
		}

		changed, err := mutate(p)
		if err != nil {
			return err
		}

		if changed {
			if err := tx.Participants().Update(ctx, p); err != nil {
				return err
			}
			if err := tx.Outbox().Enqueue(ctx, appcatalog.SyncTenantTask(tenantID, s.clock.Now())); err != nil {
				return err
			}
		}

		view, err = withTenant(ctx, tx, p)

		return err
	})

	return view, err
}

func withTenant(ctx context.Context, tx ports.Tx, p *participant.Participant) (View, error) {
	tenant, err := tx.Registry().Get(ctx, p.TenantID())
	if err != nil {
		return View{}, err
	}

	return viewOf(p, tenant), nil
}

func viewOf(p *participant.Participant, tenant *participant.Tenant) View {
	return View{
		Participant:  p.State(),
		TenantStatus: tenant.Status(),
		Visibility:   participant.VisibilityOf(p, tenant),
	}
}

// SetDelivery сохраняет покрытие через общий транзакционный сценарий и outbox.
func (s *Service) SetDelivery(
	ctx context.Context,
	tenantID ids.TenantID,
	settings participant.DeliverySettings,
	version int64,
) (View, error) {
	return s.change(ctx, tenantID, func(p *participant.Participant) (bool, error) {
		return p.SetDelivery(settings, version, s.clock.Now())
	})
}
