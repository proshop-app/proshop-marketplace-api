package catalog

import (
	"context"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

// Сигналы реестра и продолжения используют одну полосу и одну позицию магазина.
// Более новая версия ждёт окончания обхода, чтобы изменения не сбрасывали курсор.
func (i *Indexer) syncTenantTaskPage(ctx context.Context, tenant ids.TenantID) error {
	var progress ports.TenantIndexProgress
	var version int64
	var states []ports.OfferState
	err := i.uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		var err error
		progress, version, err = tx.OfferIndexWork().TenantProgress(ctx, tenant)
		if err != nil {
			return err
		}
		if progress.Complete {
			if version <= progress.Target {
				return nil
			}
			progress.Target = version
			progress.After = ""
			progress.Complete = false
		}
		states, err = tx.Offers().TenantOffers(ctx, tenant, progress.After, i.batch)
		return err
	})
	if err != nil || progress.Complete {
		return err
	}

	_, skipped, err := i.writeIndexPage(ctx, states, func(docs []ports.OfferDocument) error {
		return i.put(ctx, docs)
	})
	if err != nil {
		return err
	}

	first := progress.After == ""
	progress.Complete = len(states) < i.batch
	if len(states) > 0 {
		progress.After = states[len(states)-1].Product.State().ProductID
	}
	err = i.uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		if err := tx.OfferIndexWork().SaveTenant(ctx, tenant, progress); err != nil {
			return err
		}
		if first {
			card := ports.StoreCardTask(tenant, i.clock.Now().Add(i.refreshDelay))
			if err := tx.Outbox().Enqueue(ctx, card); err != nil {
				return err
			}
		}
		if !progress.Complete || version > progress.Target {
			return tx.Outbox().Enqueue(ctx, SyncTenantTask(tenant, i.clock.Now()))
		}
		return nil
	})
	if err != nil {
		return err
	}
	i.observeIndexSkips(ctx, "tenant", "", skipped)
	return nil
}
