// Package taxonomy — загрузка версий схем и чтение таксономии для админок магазинов.
package taxonomy

import (
	"bytes"
	"context"
	"encoding/json"

	appcatalog "github.com/proshop-app/proshop-marketplace/internal/application/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

const revalidationBatch = 200

// Service атомарно меняет схему и перепроверяет проекции; индекс догоняет через outbox.
type Service struct {
	uow   ports.UnitOfWork
	clock ports.Clock
}

// NewService собирает сценарии поверх транзакций и часов.
func NewService(uow ports.UnitOfWork, clock ports.Clock) *Service {
	return &Service{uow: uow, clock: clock}
}

// Current возвращает согласованный снимок текущей версии.
func (s *Service) Current(ctx context.Context) (taxonomy.Definition, error) {
	var d taxonomy.Definition
	err := s.uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		var err error
		d, err = tx.Taxonomy().Current(ctx)
		return err
	})
	if d.Categories == nil {
		d.Categories = []taxonomy.Category{}
	}
	return d, err
}

// Load — та же версия с теми же данными безопасна; версия назад или иные данные — конфликт.
func (s *Service) Load(ctx context.Context, d taxonomy.Definition) error {
	var err error
	d, err = s.hydrated(ctx, d)
	if err != nil {
		return err
	}
	if err := d.Validate(); err != nil {
		return err
	}
	return s.uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		r := tx.Taxonomy()
		if err := r.Lock(ctx); err != nil {
			return err
		}
		old, err := r.Current(ctx)
		if err != nil {
			return err
		}
		if d.Version <= old.Version {
			a, _ := json.Marshal(d)
			b, _ := json.Marshal(old)
			if bytes.Equal(a, b) {
				return nil
			}
			return taxonomy.ErrConflict
		}
		if err := d.CheckEvolution(old); err != nil {
			return err
		}
		if err := r.Save(ctx, d, old.Version); err != nil {
			return err
		}
		return s.revalidate(ctx, tx, d)
	})
}

func (s *Service) revalidate(ctx context.Context, tx ports.Tx, d taxonomy.Definition) error {
	var tenant ids.TenantID
	var product ids.ProductID
	for {
		items, err := tx.Products().Page(ctx, tenant, product, revalidationBatch)
		if err != nil {
			return err
		}
		for _, p := range items {
			state := p.State()
			tenant, product = state.TenantID, state.ProductID
			if !p.Revalidate(d, s.clock.Now()) {
				continue
			}
			if err := tx.Products().Save(ctx, p); err != nil {
				return err
			}
			task := appcatalog.SyncOfferTask(tenant, product, s.clock.Now())
			if err := tx.Outbox().Enqueue(ctx, task); err != nil {
				return err
			}
		}
		if len(items) < revalidationBatch {
			return nil
		}
	}
}
