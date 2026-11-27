package catalog

import (
	"context"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

// RebuildResult — итог перестроения.
type RebuildResult struct {
	Generation string
	Indexed    int
	CaughtUp   int
	Skipped    int
}

// Rebuild собирает индекс: основное наполнение, догоняющий проход и переключение.
func (i *Indexer) Rebuild(ctx context.Context) (RebuildResult, error) {
	var result RebuildResult
	err := i.lock.WithLock(ctx, func(ctx context.Context) error {
		var err error
		result, err = i.rebuild(ctx)
		return err
	})
	return result, err
}

func (i *Indexer) rebuild(ctx context.Context) (RebuildResult, error) {
	started := i.clock.Now()
	generation, err := i.index.NewGeneration(ctx)
	if err != nil {
		return RebuildResult{}, err
	}

	result := RebuildResult{Generation: generation}
	var progress ports.IndexRebuildProgress
	err = i.uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		var err error
		progress, err = tx.OfferIndexWork().BeginRebuild(ctx, generation, started)
		return err
	})
	if err != nil {
		return result, err
	}

	result.Indexed, result.Skipped = progress.Indexed, progress.Skipped
	put := func(docs []ports.OfferDocument) error {
		return i.index.PutGeneration(ctx, generation, docs)
	}
	for !progress.Complete {
		next, err := i.rebuildPage(ctx, progress, put)
		if err != nil {
			return result, err
		}
		progress = next
		result.Indexed, result.Skipped = progress.Indexed, progress.Skipped
	}

	if err := i.catchUp(ctx, progress, put, &result); err != nil {
		return result, err
	}
	return result, i.index.Activate(ctx, generation)
}

func (i *Indexer) rebuildPage(
	ctx context.Context,
	progress ports.IndexRebuildProgress,
	put func([]ports.OfferDocument) error,
) (ports.IndexRebuildProgress, error) {
	var states []ports.TileState
	err := i.uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		var err error
		states, err = tx.Offers().TilePage(ctx, ports.TilePage{After: tileCursor(progress.After), Limit: i.batch})
		return err
	})
	if err != nil {
		return progress, err
	}

	count, skipped, err := i.writeIndexPage(ctx, states, put)
	if err != nil {
		return progress, err
	}

	next := progress
	next.Indexed += count
	next.Skipped += len(skipped)
	next.Complete = len(states) < i.batch
	next.After = lastOfferKey(states, progress.After)
	err = i.uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		return tx.OfferIndexWork().SaveRebuild(ctx, progress, next)
	})
	if err != nil {
		return progress, err
	}
	i.observeIndexSkips(ctx, "rebuild", progress.Generation, skipped)
	return next, nil
}

func (i *Indexer) catchUp(
	ctx context.Context,
	progress ports.IndexRebuildProgress,
	put func([]ports.OfferDocument) error,
	result *RebuildResult,
) error {
	since := progress.Started.Add(-CatchUpMargin)
	var after ports.OfferKey
	for {
		var states []ports.TileState
		err := i.uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
			var err error
			states, err = tx.Offers().TilePage(ctx, ports.TilePage{
				After: tileCursor(after),
				Since: since,
				Limit: i.batch,
			})
			return err
		})
		if err != nil || len(states) == 0 {
			return err
		}

		count, skipped, err := i.writeIndexPage(ctx, states, put)
		if err != nil {
			return err
		}
		result.CaughtUp += count
		result.Skipped += len(skipped)
		i.observeIndexSkips(ctx, "catch_up", progress.Generation, skipped)

		if len(states) < i.batch {
			return nil
		}
		after = lastOfferKey(states, after)
	}
}

func lastOfferKey(states []ports.TileState, fallback ports.OfferKey) ports.OfferKey {
	if len(states) == 0 {
		return fallback
	}
	last := states[len(states)-1].Key
	return ports.OfferKey{TenantID: last.TenantID, ProductID: ids.ProductID(last.Key)}
}

func tileCursor(key ports.OfferKey) ports.TileKey {
	return ports.TileKey{TenantID: key.TenantID, Key: string(key.ProductID)}
}
