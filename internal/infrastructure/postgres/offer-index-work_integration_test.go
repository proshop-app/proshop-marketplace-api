//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/postgres"
)

func TestOfferIndexWorkResumesAndFencesStaleWorkers(t *testing.T) {
	pool := testPool(t)
	uow := postgres.NewUnitOfWork(pool)
	ctx := context.Background()
	rollback := errors.New("страница откатилась")
	var initial, saved ports.IndexRebuildProgress
	save := func(ctx context.Context, tx ports.Tx) error {
		work := tx.OfferIndexWork()
		var err error
		initial, err = work.BeginRebuild(ctx, "gen-1", at(0))
		if err != nil {
			return err
		}
		if initial.After != (ports.OfferKey{}) || initial.Indexed != 0 || initial.Skipped != 0 {
			return fmt.Errorf("после отката курсор продвинулся: %+v", initial)
		}
		saved = initial
		saved.After = ports.OfferKey{TenantID: "t1", ProductID: "p2"}
		saved.Indexed, saved.Skipped = 1, 1
		return work.SaveRebuild(ctx, initial, saved)
	}
	err := uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		if err := save(ctx, tx); err != nil {
			return err
		}
		return rollback
	})
	if !errors.Is(err, rollback) {
		t.Fatal(err)
	}
	if err := uow.Do(ctx, save); err != nil {
		t.Fatal(err)
	}

	replica := postgres.NewUnitOfWork(pool)
	err = replica.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		work := tx.OfferIndexWork()
		resumed, err := work.BeginRebuild(ctx, saved.Generation, at(10))
		if err != nil || resumed != saved {
			return fmt.Errorf("реплика потеряла прогресс или начало обхода: %+v: %w", resumed, err)
		}
		if err := work.SaveRebuild(ctx, initial, saved); !errors.Is(err, ports.ErrOfferIndexProgressConflict) {
			return fmt.Errorf("старый владелец страницы: %w", err)
		}
		replacement, err := work.BeginRebuild(ctx, "gen-2", at(10))
		if err != nil || replacement.Generation != "gen-2" || replacement.After != (ports.OfferKey{}) ||
			replacement.Indexed != 0 || replacement.Skipped != 0 {
			return fmt.Errorf("курсор другого поколения: %+v: %w", replacement, err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	err = replica.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		work := tx.OfferIndexWork()
		current, version, err := work.TenantProgress(ctx, "t1")
		if err != nil || version != 0 || !current.Complete {
			return fmt.Errorf("новый курсор магазина: %+v, версия %d: %w", current, version, err)
		}
		current.Target, current.After, current.Complete = 2, "p2", false
		if err := work.SaveTenant(ctx, "t1", current); err != nil {
			return err
		}
		if err := work.SaveTenant(ctx, "t1", current); !errors.Is(err, ports.ErrOfferIndexProgressConflict) {
			return fmt.Errorf("старый владелец страницы магазина: %w", err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
