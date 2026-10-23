package buyer

import (
	"context"

	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

// Repository — покупатели.
type Repository interface {
	// FindByPhone — покупатель по телефону; нет — ErrNotFound.
	FindByPhone(ctx context.Context, phone Phone) (Buyer, error)
	// Get — покупатель; нет — ErrNotFound.
	Get(ctx context.Context, id ids.UUID) (Buyer, error)
	// Add заводит покупателя; телефон занят — ErrExists.
	Add(ctx context.Context, buyer Buyer) error
}

// ChallengeRepository — проверки входа.
type ChallengeRepository interface {
	// Get — проверка; нет — ErrChallengeNotFound.
	Get(ctx context.Context, id ids.UUID) (*Challenge, error)
	// FindByProviderRef — проверка по идентификатору шлюза; нет — ErrChallengeNotFound.
	FindByProviderRef(ctx context.Context, ref string) (*Challenge, error)
	// Add сохраняет новую проверку.
	Add(ctx context.Context, challenge *Challenge) error
	// Update сохраняет условно по исходным статусу и версии; не совпали — ErrChallengeConflict.
	Update(ctx context.Context, challenge *Challenge) error
}

// SessionRepository — сессии покупателей с собственным сроком жизни.
type SessionRepository interface {
	// Save сохраняет сессию до её ExpiresAt.
	Save(ctx context.Context, session Session) error
	// Get — сессия по отпечатку токена; нет или истекла — ErrSessionNotFound.
	Get(ctx context.Context, tokenHash string) (Session, error)
	// Delete удаляет сессию; отсутствующая — не ошибка.
	Delete(ctx context.Context, tokenHash string) error
}
