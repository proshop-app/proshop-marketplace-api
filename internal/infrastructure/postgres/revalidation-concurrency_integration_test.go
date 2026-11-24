//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	appcatalog "github.com/proshop-app/proshop-marketplace/internal/application/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	apptaxonomy "github.com/proshop-app/proshop-marketplace/internal/application/taxonomy"
	"github.com/proshop-app/proshop-marketplace/internal/domain/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/clock"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/postgres"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/postgres/pgtest"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

type heldRevalidationUOW struct {
	ports.UnitOfWork
	calls   int
	locked  chan struct{}
	release chan struct{}
}

type signalledProductUOW struct {
	ports.UnitOfWork
	saving chan struct{}
	once   sync.Once
}

type signalledProductTx struct {
	ports.Tx
	owner *signalledProductUOW
}

type signalledProducts struct {
	catalog.ProductRepository
	owner *signalledProductUOW
}

func (u *heldRevalidationUOW) Do(ctx context.Context, fn func(context.Context, ports.Tx) error) error {
	u.calls++
	return u.UnitOfWork.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		if err := fn(ctx, tx); err != nil {
			return err
		}
		if u.calls == 2 {
			close(u.locked)
			select {
			case <-u.release:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		return nil
	})
}

func (u *signalledProductUOW) Do(ctx context.Context, fn func(context.Context, ports.Tx) error) error {
	return u.UnitOfWork.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		return fn(ctx, signalledProductTx{Tx: tx, owner: u})
	})
}

func (t signalledProductTx) Products() catalog.ProductRepository {
	return signalledProducts{ProductRepository: t.Tx.Products(), owner: t.owner}
}

func (r signalledProducts) Save(ctx context.Context, product *catalog.Product) error {
	r.owner.once.Do(func() { close(r.owner.saving) })
	return r.ProductRepository.Save(ctx, product)
}

// SaveMany — запись товаров транзакцией пачки приёма: тот же момент, что и Save.
func (r signalledProducts) SaveMany(ctx context.Context, products []*catalog.Product) error {
	r.owner.once.Do(func() { close(r.owner.saving) })
	return r.ProductRepository.SaveMany(ctx, products)
}

func TestTaxonomyPageAndReverseEventOrderDoNotDeadlock(t *testing.T) {
	pool := pgtest.Pool(t)
	uow := postgres.NewUnitOfWork(pool)
	service, definition := prepareModeratedRevalidation(t, uow, 50)
	definition.Attributes[0].Name = "Изменённое поле"
	if _, err := service.LoadPart(context.Background(), "attributes", definition, 1); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	held := &heldRevalidationUOW{UnitOfWork: uow, locked: make(chan struct{}), release: make(chan struct{})}
	worker := apptaxonomy.NewService(held, clock.System{}, nil)
	workerDone := make(chan error, 1)
	go func() {
		_, err := worker.RevalidateNext(ctx)
		workerDone <- err
	}()
	select {
	case <-held.locked:
	case err := <-workerDone:
		t.Fatal(err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	receipt := &signalledProductUOW{UnitOfWork: uow, saving: make(chan struct{})}
	projector := appcatalog.NewProjector(receipt, clock.System{}, nil)
	receiptDone := make(chan error, 1)
	go func() { receiptDone <- applyReverseTaxonomyEvents(ctx, projector) }()
	select {
	case <-receipt.saving:
		close(held.release)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if err := <-workerDone; err != nil {
		t.Fatal(err)
	}
	if err := <-receiptDone; err != nil {
		t.Fatal(err)
	}
	err := uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		for _, id := range []ids.ProductID{"p000", "p049"} {
			product, err := tx.Products().Get(ctx, "t1", id)
			if err != nil {
				return err
			}
			state := product.State()
			if state.Revision != 2 || state.Card.Publication.Version != 2 {
				return fmt.Errorf("потеряно событие %s", id)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

}

func applyReverseTaxonomyEvents(ctx context.Context, projector *appcatalog.Projector) error {
	// В mp-ingest групповым является Kafka Commit, а ApplyProduct по-прежнему отдельная транзакция.
	for _, product := range []ids.ProductID{"p049", "p000"} {
		event := appcatalog.ProductEvent{
			Snapshot: catalog.Snapshot{
				TenantID:  "t1",
				ProductID: product,
				Revision:  2,
				At:        time.Now(),
				Card: catalog.Card{
					Name:        "Товар",
					Visible:     true,
					Marketplace: &taxonomy.Card{CategoryID: "c1", Published: true},
				},
			},
		}
		err := projector.ApplyProduct(ctx, event)
		if errors.Is(err, catalog.ErrConflict) {
			err = projector.ApplyProduct(ctx, event)
		}
		if err != nil {
			return err
		}
	}
	return nil
}
