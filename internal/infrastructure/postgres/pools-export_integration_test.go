//go:build integration

package postgres

import (
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
)

// Pool открывает тестам pgxpool класса: занять квоту класса целиком и прочитать
// параметры сеанса его соединения.
func (p *Pools) Pool(class ports.DatabaseClass) *pgxpool.Pool {
	return p.classes[class].pool
}
