package fake

import (
	"context"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/catalog"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

// ProductConflict имитирует сохранение параллельного писателя после чтения страницы.
// Используется последовательно: счётчик не является частью откатываемой транзакции.
type ProductConflict struct {
	ports.UnitOfWork
	Offer     ids.OfferID
	Conflicts int
}

type conflictTx struct {
	ports.Tx
	owner *ProductConflict
}

type conflictProducts struct {
	catalog.ProductRepository
	owner *ProductConflict
}

// Do подменяет только репозиторий товаров, сохраняя атомарность исходного хранилища.
func (u *ProductConflict) Do(ctx context.Context, fn func(context.Context, ports.Tx) error) error {
	return u.UnitOfWork.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		return fn(ctx, conflictTx{Tx: tx, owner: u})
	})
}

func (tx conflictTx) Products() catalog.ProductRepository {
	return conflictProducts{ProductRepository: tx.Tx.Products(), owner: tx.owner}
}

func (r conflictProducts) Save(ctx context.Context, p *catalog.Product) error {
	state := p.State()
	if r.owner.Conflicts == 0 && state.TenantID == r.owner.Offer.Tenant && state.ProductID == r.owner.Offer.Product {
		current, err := r.Get(ctx, state.TenantID, state.ProductID)
		if err != nil {
			return err
		}
		current.SetModelKey("parallel-writer", "test")
		if err := r.ProductRepository.Save(ctx, current); err != nil {
			return err
		}
		r.owner.Conflicts++
		return catalog.ErrConflict
	}
	return r.ProductRepository.Save(ctx, p)
}
