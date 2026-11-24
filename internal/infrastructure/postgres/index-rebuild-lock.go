package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/pkg/safego"
)

const (
	indexRebuildLockName      = "uno:marketplace:offer-index-rebuild"
	acquireIndexRebuildLock   = "SELECT pg_try_advisory_lock(hashtextextended($1, 0))"
	indexRebuildProbeInterval = time.Second
)

// IndexRebuildLock — общая именованная advisory lock для CLI и фоновых процессов.
// Работа под ней идёт многими транзакциями, поэтому блокировка сеансовая и держится
// отдельным прямым сеансом: он не занимает квоту пулов и закрывается после работы.
type IndexRebuildLock struct {
	name     string
	sessions *DirectSessions
}

// NewIndexRebuildLock привязывает владение к отдельному прямому сеансу PostgreSQL.
func NewIndexRebuildLock(sessions *DirectSessions) *IndexRebuildLock {
	return &IndexRebuildLock{sessions: sessions, name: indexRebuildLockName}
}

// WithLock не ждёт занятую блокировку: фоновый цикл повторит попытку позже.
func (l *IndexRebuildLock) WithLock(ctx context.Context, run func(context.Context) error) error {
	session, err := l.sessions.open(ctx)
	if err != nil {
		return err
	}
	defer closeSession(ctx, session)

	var acquired bool
	if err := session.QueryRow(ctx, acquireIndexRebuildLock, l.name).Scan(&acquired); err != nil {
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
	// Паника проверки владельца снимает работу: без проверки блокировку держать нельзя.
	safego.Run(
		"postgres.lock-probe",
		func() error {
			defer close(stopped)
			ticker := time.NewTicker(indexRebuildProbeInterval)
			defer ticker.Stop()

			for {
				select {
				case <-work.Done():
					return nil
				case <-ticker.C:
					probe, stop := context.WithTimeout(work, indexRebuildProbeInterval)
					err := session.Ping(probe)
					stop()

					if err != nil {
						cancel()
						return nil
					}
				}
			}
		},
		func(err error) {
			if err != nil {
				cancel()
			}
		},
	)

	if err := run(work); err != nil {
		return err
	}

	return work.Err()
}
