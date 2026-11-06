package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
)

const (
	indexRebuildLockName       = "uno:marketplace:offer-index-rebuild"
	acquireIndexRebuildLock    = "SELECT pg_try_advisory_lock(hashtextextended($1, 0))"
	indexRebuildProbeInterval  = time.Second
	indexRebuildCleanupTimeout = 5 * time.Second
)

// IndexRebuildLock — блокировка пересборки, общая для CLI и фоновых процессов.
// Выделенное соединение закрывается, поэтому session lock не остаётся в пуле.
type IndexRebuildLock struct {
	name string
	pool *pgxpool.Pool
}

// NewIndexRebuildLock привязывает владение к выделенному соединению PostgreSQL.
func NewIndexRebuildLock(pool *pgxpool.Pool) *IndexRebuildLock {
	return &IndexRebuildLock{
		pool: pool,
		name: indexRebuildLockName,
	}
}

// WithLock не ждёт занятую блокировку: фоновый цикл повторит попытку позже.
func (l *IndexRebuildLock) WithLock(ctx context.Context, run func(context.Context) error) error {
	pooled, err := l.pool.Acquire(ctx)

	if err != nil {
		return err
	}
	connection := pooled.Hijack()
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), indexRebuildCleanupTimeout)
		defer cancel()
		_ = connection.Close(cleanup)
	}()

	var acquired bool
	err = connection.QueryRow(ctx, acquireIndexRebuildLock, l.name).
		Scan(&acquired)

	if err != nil {
		return fmt.Errorf("блокировка пересборки индекса: %w", err)
	}

	if !acquired {
		return ports.ErrRebuildBusy
	}
	// Вызывающий получает отмену при потере владельца, не только при остановке процесса.
	work, cancel := context.WithCancel(ctx)
	defer cancel()
	stopped := make(chan struct{})
	defer func() {
		cancel()
		<-stopped
	}()
	go func() {
		defer close(stopped)
		ticker := time.NewTicker(indexRebuildProbeInterval)
		defer ticker.Stop()

		for {
			select {
			case <-work.Done():
				return
			case <-ticker.C:
				probe, stop := context.WithTimeout(work, indexRebuildProbeInterval)
				err := connection.Ping(probe)
				stop()

				if err != nil {
					cancel()
					return
				}
			}
		}
	}()

	if err := run(work); err != nil {
		return err
	}

	return work.Err()
}

// NewPromotionLock отделяет цикл продвижения от долгой пересборки индекса.
func NewPromotionLock(pool *pgxpool.Pool) *IndexRebuildLock {
	return &IndexRebuildLock{pool: pool, name: "uno:marketplace:promotion"}
}
