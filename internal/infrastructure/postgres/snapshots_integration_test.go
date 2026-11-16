//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"testing"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/moderation"
	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/postgres"
)

func TestSnapshotsDoNotPublishRolledBackWrites(t *testing.T) {
	pool := testPool(t)
	snapshots := &postgres.Snapshots{}
	writer := postgres.NewUnitOfWorkWithSnapshots(pool, snapshots)
	reader := postgres.NewUnitOfWorkWithSnapshots(pool, snapshots)
	ctx := context.Background()
	if err := writer.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		return tx.Taxonomy().Save(ctx, testTaxonomy(1), 0)
	}); err != nil {
		t.Fatal(err)
	}
	checkCommittedSnapshots(t, reader, 1, "Категория", 0)
	abort := errors.New("откат загрузки")
	err := writer.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		if err := tx.Taxonomy().Lock(ctx); err != nil {
			return err
		}
		next := testTaxonomy(2)
		next.Categories[0].Name = "Отменено"
		if err := tx.Taxonomy().Save(ctx, next, 1); err != nil {
			return err
		}
		phrase := moderation.StopPhrase{Key: "phrase", Text: "Отменено"}
		if err := tx.ModerationRules().AddPhrase(ctx, phrase); err != nil {
			return err
		}
		d, err := tx.Taxonomy().Current(ctx)
		if err != nil {
			return err
		}
		rules, err := tx.ModerationRules().Current(ctx)
		if err != nil {
			return err
		}
		if d.Version != 2 || rules.Revision != 2 || rules.Phrases[0].Text != "Отменено" {
			t.Fatal("транзакция не прочитала собственные записи")
		}
		return abort
	})
	if !errors.Is(err, abort) {
		t.Fatal(err)
	}
	checkCommittedSnapshots(t, reader, 1, "Категория", 0)
	if err := writer.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		next := testTaxonomy(2)
		next.Categories[0].Name = "Зафиксировано"
		if err := tx.Taxonomy().Save(ctx, next, 1); err != nil {
			return err
		}
		return tx.ModerationRules().AddPhrase(ctx, moderation.StopPhrase{Key: "phrase", Text: "Зафиксировано"})
	}); err != nil {
		t.Fatal(err)
	}
	checkCommittedSnapshots(t, reader, 2, "Зафиксировано", 1)
}

func checkCommittedSnapshots(t *testing.T, uow ports.UnitOfWork, version int64, name string, phrases int) {
	t.Helper()
	var d taxonomy.Definition
	var rules moderation.Rules
	err := uow.Do(context.Background(), func(ctx context.Context, tx ports.Tx) error {
		var err error
		d, err = tx.Taxonomy().Current(ctx)
		if err != nil {
			return err
		}
		rules, err = tx.ModerationRules().Current(ctx)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if d.Version != version || d.Categories[0].Name != name || rules.Revision != version ||
		len(rules.Phrases) != phrases || phrases > 0 && rules.Phrases[0].Text != name {
		t.Fatalf("таксономия: %+v; правила: %+v", d, rules)
	}
}
