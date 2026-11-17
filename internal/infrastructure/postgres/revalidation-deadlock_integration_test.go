//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/postgres"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/postgres/pgtest"
)

const (
	raiseRevalidationDeadlock = `CREATE FUNCTION revalidation_deadlock() RETURNS trigger LANGUAGE plpgsql AS $$
    BEGIN
        IF NEW.product_id = 'p025' THEN
            RAISE EXCEPTION 'injected deadlock' USING ERRCODE = '40P01';
        END IF;
        RETURN NEW;
    END $$;
    CREATE TRIGGER revalidation_deadlock BEFORE INSERT ON moderation_cases
        FOR EACH ROW EXECUTE FUNCTION revalidation_deadlock()`
	removeRevalidationDeadlock = `DROP TRIGGER revalidation_deadlock ON moderation_cases`
	readRevalidationAtomicity  = `SELECT
    (SELECT count(*) FROM catalog_products WHERE card->'publication'->>'version' = '2'),
    (SELECT count(*) FROM moderation_verdicts),
    (SELECT count(*) FROM moderation_cases),
    (SELECT count(*) FROM moderation_verdict_log),
    (SELECT count(*) FROM outbox),
    (SELECT count(*) FROM taxonomy_revalidation_failures),
    (SELECT processed FROM taxonomy_revalidation WHERE version = 2 AND category = 'c1')`
)

func TestTaxonomyDeadlockRollsBackWholePageAndRetries(t *testing.T) {
	ctx := context.Background()
	pool := pgtest.Pool(t)
	uow := postgres.NewUnitOfWork(pool)
	service, definition := prepareModeratedRevalidation(t, uow, 50)
	definition.Attributes[0].Name = "Изменённое поле"
	if _, err := service.LoadPart(ctx, "attributes", definition, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, raiseRevalidationDeadlock); err != nil {
		t.Fatal(err)
	}
	_, err := service.RevalidateNext(ctx)
	var pgError *pgconn.PgError
	if !errors.As(err, &pgError) || pgError.Code != "40P01" {
		t.Fatal("не возвращён исходный deadlock", err)
	}
	values := [7]int{}
	err = pool.QueryRow(ctx, readRevalidationAtomicity).Scan(
		&values[0],
		&values[1],
		&values[2],
		&values[3],
		&values[4],
		&values[5],
		&values[6],
	)
	if err != nil || values != [7]int{} {
		t.Fatal("deadlock оставил часть страницы или продвинул курсор", values, err)
	}
	job, err := service.RevalidateNext(ctx)
	if err != nil || job == nil || job.Category != "c2" {
		t.Fatal("deadlock остановил другое задание", job, err)
	}
	if _, err := pool.Exec(ctx, removeRevalidationDeadlock); err != nil {
		t.Fatal(err)
	}
	time.Sleep(1100 * time.Millisecond)
	job, err = service.RevalidateNext(ctx)
	if err != nil || job == nil || job.Category != "c1" || job.Processed != 50 {
		t.Fatal("страница не взята повторно с прежнего курсора", job, err)
	}
	drainTaxonomyPages(t, service)
	failures, err := service.RevalidationFailures(ctx, ports.OfferKey{}, 100)
	if err != nil || len(failures) != 0 {
		t.Fatal("временная ошибка записана как постоянная", failures, err)
	}
}
