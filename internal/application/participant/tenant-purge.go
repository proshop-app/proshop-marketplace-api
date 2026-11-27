package participant

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

const tenantPurgeBatch = 200

// TenantPurger выполняет отложенное удаление магазина по сохранённой позиции.
type TenantPurger struct {
	uow   ports.UnitOfWork
	index ports.OfferIndex
	clock ports.Clock
}

type tenantPurgeTask struct {
	Step      int64 `json:"step"`
	IndexDone bool  `json:"indexDone,omitempty"`
}

// NewTenantPurger подключает очистку PostgreSQL и поискового представления.
func NewTenantPurger(uow ports.UnitOfWork, index ports.OfferIndex, clock ports.Clock) *TenantPurger {
	return &TenantPurger{uow: uow, index: index, clock: clock}
}

func startTenantPurge(ctx context.Context, tx ports.Tx, event participant.RegistryEvent, now time.Time) error {
	if _, err := ids.ParseTenantID(string(event.TenantID)); err != nil || event.At.IsZero() {
		return participant.ErrInvalidRegistryEvent
	}
	started, err := tx.TenantPurge().Begin(ctx, event.TenantID)
	if err != nil || !started {
		return err
	}

	tenant, err := tx.Registry().Get(ctx, event.TenantID)
	switch {
	case err == nil:
		if tenant.Delete(event.At, now) {
			if err := tx.Registry().Save(ctx, tenant); err != nil {
				return err
			}
		}
	case !errors.Is(err, participant.ErrTenantNotFound):
		return err
	}
	if err := tx.StoreCards().Hide(ctx, event.TenantID); err != nil {
		return err
	}
	return enqueueTenantPurge(ctx, tx, event.TenantID, ports.TenantPurgeCursor{}, now, time.Time{})
}

func enqueueTenantPurge(
	ctx context.Context, tx ports.Tx, tenant ids.TenantID, cursor ports.TenantPurgeCursor, at, availableAt time.Time,
) error {
	payload, _ := json.Marshal(tenantPurgeTask{Step: cursor.Step, IndexDone: cursor.IndexDone})
	return tx.Outbox().Enqueue(ctx, ports.NewTask{
		Kind:        ports.TaskPurgeTenant,
		TenantID:    tenant,
		DedupKey:    ports.TaskPurgeTenant + ":" + string(tenant),
		Payload:     payload,
		At:          at,
		AvailableAt: availableAt,
	})
}

// HandleTask подтверждает одну порцию и ставит продолжение в той же транзакции.
func (p *TenantPurger) HandleTask(ctx context.Context, task ports.Task) error {
	var payload tenantPurgeTask
	if task.Kind != ports.TaskPurgeTenant || task.TenantID == "" || json.Unmarshal(task.Payload, &payload) != nil {
		return ErrInvalidTask
	}

	tenant := task.TenantID
	if payload.IndexDone {
		return p.advance(ctx, tenant, payload.Step, nil, true)
	}
	var products []ids.ProductID
	var current bool
	err := p.uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		var err error
		products, current, err = tx.TenantPurge().IndexPage(ctx, tenant, payload.Step, tenantPurgeBatch+1)
		return err
	})
	if err != nil || !current {
		return err
	}

	last := len(products) <= tenantPurgeBatch
	products = products[:min(tenantPurgeBatch, len(products))]
	if err := p.withdrawOffers(ctx, tenant, products); err != nil {
		return err
	}
	return p.advance(ctx, tenant, payload.Step, products, last)
}

func (p *TenantPurger) withdrawOffers(ctx context.Context, tenant ids.TenantID, products []ids.ProductID) error {
	if len(products) == 0 {
		return nil
	}
	docs := make([]ports.OfferDocument, 0, len(products))
	now := p.clock.Now()
	for _, product := range products {
		id := ids.OfferID{Tenant: tenant, Product: product}
		// Снимок, прочитанный до удаления, не сможет вернуть карточку ни в одно поколение.
		docs = append(docs, ports.OfferDocument{
			ID:        id.String(),
			TenantID:  string(tenant),
			ProductID: string(product),
			Version:   math.MaxInt64,
			UpdatedAt: now,
		})
	}
	return p.index.Put(ctx, docs)
}

func (p *TenantPurger) advance(
	ctx context.Context, tenant ids.TenantID, step int64, products []ids.ProductID, indexDone bool,
) error {
	return p.uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		repo := tx.TenantPurge()
		cursor, err := repo.LockCursor(ctx, tenant)
		if err != nil || cursor.Complete || cursor.Step != step {
			return err
		}
		resume, err := purgeTenantPage(ctx, repo, tenant, &cursor, products, indexDone)
		if err != nil {
			return err
		}
		cursor.Step++
		if err := repo.Advance(ctx, tenant, cursor); err != nil || cursor.Complete {
			return err
		}
		return enqueueTenantPurge(ctx, tx, tenant, cursor, p.clock.Now(), resume)
	})
}

func purgeTenantPage(
	ctx context.Context, repo ports.TenantPurge, tenant ids.TenantID, cursor *ports.TenantPurgeCursor,
	products []ids.ProductID, indexDone bool,
) (time.Time, error) {
	if !cursor.IndexDone {
		if len(products) > 0 {
			cursor.Product = products[len(products)-1]
		}
		cursor.IndexDone = indexDone
		if !cursor.IndexDone {
			return time.Time{}, nil
		}
	}
	resume, err := repo.DeletePage(ctx, tenant, tenantPurgeBatch)
	if err != nil || !resume.IsZero() {
		return resume, err
	}
	cursor.Complete = true
	cursor.Product = ""
	return time.Time{}, repo.Finish(ctx, tenant)
}
