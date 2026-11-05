//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	app "github.com/proshop-app/proshop-marketplace/internal/application/taxonomy"
	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/clock"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/postgres"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/postgres/pgtest"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/taxonomydata"
)

func TestReleaseFullBundleAndConcurrentRetry(t *testing.T) {
	pool := pgtest.Pool(t)
	ctx := context.Background()
	release, _, err := taxonomydata.Load()
	if err != nil {
		t.Fatal(err)
	}
	service := app.NewService(postgres.NewUnitOfWork(pool), clock.System{}, nil)
	results := make(chan taxonomy.ReleaseResult, 2)
	failures := make(chan error, 2)
	var group sync.WaitGroup
	for i := 0; i < 2; i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			result, err := service.Apply(ctx, release)
			results <- result
			failures <- err
		}()
	}
	group.Wait()
	close(results)
	close(failures)
	for err := range failures {
		if err != nil {
			t.Fatal(err)
		}
	}
	changed := 0
	for result := range results {
		if result.Changed {
			changed++
		}
		if result.SnapshotVersion != 1 {
			t.Fatalf("неожиданная версия: %+v", result)
		}
	}
	snapshot, err := app.NewService(postgres.NewUnitOfWork(pool), clock.System{}, nil).Current(ctx)
	if err != nil || changed != 1 || len(snapshot.Categories) != len(release.Categories) {
		t.Fatalf("повтор/перезапуск: changed=%d, version=%d, err=%v", changed, snapshot.Version, err)
	}
	var count int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM taxonomy_releases").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("дубли поставки: %d", count)
	}
}

func TestReleaseUpdateAndRollback(t *testing.T) {
	pool := pgtest.Pool(t)
	ctx := context.Background()
	service := app.NewService(postgres.NewUnitOfWork(pool), clock.System{}, nil)
	repository := service
	first := testRelease(1, "a")
	second := testRelease(2, "b")
	if _, err := service.Apply(ctx, first); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Apply(ctx, second); err != nil {
		t.Fatal(err)
	}
	result, err := service.Rollback(ctx, second)
	if err != nil || !result.Changed || result.SnapshotVersion != 3 {
		t.Fatalf("откат: %+v, %v", result, err)
	}
	snapshot, err := repository.Current(ctx)
	if err != nil || snapshot.Categories[0].ID != first.Categories[0].ID {
		t.Fatalf("не восстановлен исходный снимок: %+v, %v", snapshot, err)
	}
	if result, err := service.Rollback(ctx, second); err != nil || result.Changed {
		t.Fatalf("повтор отката: %+v, %v", result, err)
	}
	if _, err := service.Apply(ctx, second); !errors.Is(err, taxonomy.ErrReleaseConflict) {
		t.Fatalf("отменённая поставка применена: %v", err)
	}
	if _, err := service.Apply(ctx, first); !errors.Is(err, taxonomy.ErrReleaseConflict) {
		t.Fatalf("понижение поставки разрешено: %v", err)
	}
	if _, err := service.Apply(ctx, testRelease(3, "c")); err != nil {
		t.Fatalf("новая поставка после отката: %v", err)
	}
}

func TestReleasePreservesOperatorChanges(t *testing.T) {
	pool := pgtest.Pool(t)
	ctx := context.Background()
	service := app.NewService(postgres.NewUnitOfWork(pool), clock.System{}, nil)
	repository := service
	release := testRelease(1, "a")
	if _, err := service.Apply(ctx, release); err != nil {
		t.Fatal(err)
	}
	operator := testRelease(2, "b")
	if err := repository.Load(ctx, taxonomy.Definition{Version: 2, Categories: operator.Categories}); err != nil {
		t.Fatal(err)
	}
	if result, err := service.Apply(ctx, release); err != nil || result.Changed || result.SnapshotVersion != 2 {
		t.Fatalf("повтор должен сохранить правки: %+v, %v", result, err)
	}
	if _, err := service.Apply(ctx, operator); !errors.Is(err, taxonomy.ErrReleaseConflict) {
		t.Fatalf("обновление затёрло правки: %v", err)
	}
	if _, err := service.Rollback(ctx, release); !errors.Is(err, taxonomy.ErrReleaseConflict) {
		t.Fatalf("откат затёр правки: %v", err)
	}
	snapshot, err := repository.Current(ctx)
	if err != nil || snapshot.Categories[0].ID != operator.Categories[0].ID {
		t.Fatalf("операторские данные изменены: %+v, %v", snapshot, err)
	}
	operator.ExpectedVersion = 1
	if _, err := service.Apply(ctx, operator); !errors.Is(err, taxonomy.ErrReleaseConflict) {
		t.Fatalf("неверная исходная версия принята: %v", err)
	}
	operator.ExpectedVersion = 2
	if _, err := service.Apply(ctx, operator); err != nil {
		t.Fatalf("явное обновление проверенной версии: %v", err)
	}
}

func TestReleaseFailureRollsBackSnapshot(t *testing.T) {
	pool := pgtest.Pool(t)
	ctx := context.Background()
	_, err := pool.Exec(ctx, "ALTER TABLE taxonomy_releases ADD CHECK (release <> 2)")
	if err != nil {
		t.Fatal(err)
	}
	service := app.NewService(postgres.NewUnitOfWork(pool), clock.System{}, nil)
	if _, err := service.Apply(ctx, testRelease(2, "a")); err == nil {
		t.Fatal("ошибка записи истории не возвращена")
	}
	snapshot, err := service.Current(ctx)
	if err != nil || snapshot.Version != 0 || len(snapshot.Categories) != 0 {
		t.Fatalf("частично опубликованный снимок: %+v, %v", snapshot, err)
	}
}

func testRelease(version int64, suffix string) taxonomy.Release {
	return taxonomy.Release{
		Version:  version,
		Checksum: strings.Repeat(suffix, 64),
		Categories: []taxonomy.Category{{
			ID:   "c" + suffix,
			Slug: "c" + suffix,
			Name: "Категория",
			Attributes: []taxonomy.Attribute{{
				Code: "a1",
				Name: "Атрибут",
				Type: "string",
			}},
		}},
	}
}
