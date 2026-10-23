package ports

import (
	"context"

	"github.com/proshop-app/proshop-marketplace/internal/domain/operator"
)

// OperatorSessions отделены от покупательских ключами и типом данных.
type OperatorSessions interface {
	Save(ctx context.Context, session operator.Session) error
	Get(ctx context.Context, hash string) (operator.Session, error)
	Delete(ctx context.Context, hash string) error
}

// PasswordVerifier проверяет пароль по Argon2id-хешу из конфигурации.
type PasswordVerifier interface {
	Verify(password, hash string) bool
}

// EventReplay повторно разбирает исходный конверт с прежним ключом дедупликации.
type EventReplay interface {
	Replay(ctx context.Context, event ParkedEvent) error
}
