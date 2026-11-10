//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/proshop-app/proshop-marketplace/internal/domain/buyer"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/postgres"
)

func TestIdentityMappingsWithSingleConnection(t *testing.T) {
	seedPool := testPool(t)
	config := seedPool.Config()
	config.MaxConns = 1
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	mapping := postgres.NewIdentityMappings(pool, "issuer-a", "client-a")

	if _, err := mapping.Find(ctx, "subject"); !errors.Is(err, buyer.ErrSessionNotFound) {
		t.Fatalf("неизвестный subject: %v", err)
	}
	for range 2 {
		if err := mapping.Bind(ctx, "subject", string(buyerA)); err != nil {
			t.Fatalf("повтор одной привязки: %v", err)
		}
	}
	if err := mapping.Bind(ctx, "subject", string(buyerB)); !errors.Is(err, buyer.ErrSessionNotFound) {
		t.Fatalf("перепривязка subject запрещена: %v", err)
	}
	if found, err := mapping.Find(ctx, "subject"); err != nil || found != string(buyerA) {
		t.Fatalf("исходная привязка сохранена: %q, %v", found, err)
	}

	for _, scope := range []struct {
		issuer string
		client string
	}{
		{
			issuer: "issuer-b",
			client: "client-a",
		},
		{
			issuer: "issuer-a",
			client: "client-b",
		},
	} {
		other := postgres.NewIdentityMappings(pool, scope.issuer, scope.client)
		if _, err := other.Find(ctx, "subject"); !errors.Is(err, buyer.ErrSessionNotFound) {
			t.Fatalf("scope %s/%s изолирован: %v", scope.issuer, scope.client, err)
		}
		if err := other.Bind(ctx, "subject", string(buyerB)); err != nil {
			t.Fatal(err)
		}
	}

	if err := mapping.Bind(ctx, "another-subject", string(buyerA)); !errors.Is(err, postgres.ErrIdentityStorage) {
		t.Fatalf("конфликт SQL не раскрывает параметры: %v", err)
	}
	if err := mapping.Bind(ctx, "another-subject", string(buyerB)); err != nil {
		t.Fatalf("ошибка отдельного запроса не прерывает следующие операции: %v", err)
	}
	if acquired := pool.Stat().AcquiredConns(); acquired != 0 {
		t.Fatalf("операции возвращают единственное соединение в пул: %d", acquired)
	}

	canceled, stop := context.WithCancel(ctx)
	stop()
	if _, err := mapping.Find(canceled, "subject"); !errors.Is(err, postgres.ErrIdentityStorage) {
		t.Fatalf("ошибка получения соединения скрыта: %v", err)
	}
}
