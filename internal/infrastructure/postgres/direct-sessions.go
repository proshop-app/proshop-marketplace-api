package postgres

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// closeSessionTimeout — срок закрытия сеанса после отмены работы: закрытие снимает
// сеансовую блокировку и не должно зависеть от отменённого контекста.
const closeSessionTimeout = 5 * time.Second

// DirectSessions открывает отдельные прямые сеансы PostgreSQL в обход пулера и вне
// пулов классов. Сеансовая advisory lock живёт, пока жив сеанс: в режиме транзакций
// пулер отдал бы серверное соединение другому клиенту вместе с блокировкой.
type DirectSessions struct {
	config *pgx.ConnConfig
}

// NewDirectSessions — сеансы по прямому адресу базы со сроками budget.
func NewDirectSessions(url string, budget Budget) (*DirectSessions, error) {
	if err := validateBudget(budget); err != nil {
		return nil, err
	}
	cfg, err := poolConfig(url, 1)
	if err != nil {
		return nil, err
	}
	applyBudget(cfg, budget)
	return &DirectSessions{config: cfg.ConnConfig}, nil
}

// DirectSessionsOf — сеансы с настройками пула, открытого напрямую (Open, OpenCommand):
// CLI и тесты.
func DirectSessionsOf(pool *pgxpool.Pool) *DirectSessions {
	return &DirectSessions{config: pool.Config().ConnConfig}
}

func (s *DirectSessions) open(ctx context.Context) (*pgx.Conn, error) {
	conn, err := pgx.ConnectConfig(ctx, s.config.Copy())
	if err != nil {
		return nil, ErrUnavailable.Wrap(err)
	}
	return conn, nil
}

// closeSession закрывает сеанс и тем снимает его блокировки.
func closeSession(ctx context.Context, conn *pgx.Conn) {
	closing, cancel := context.WithTimeout(context.WithoutCancel(ctx), closeSessionTimeout)
	defer cancel()
	_ = conn.Close(closing)
}
