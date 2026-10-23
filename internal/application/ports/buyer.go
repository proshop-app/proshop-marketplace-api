package ports

import (
	"context"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/domain/buyer"
)

// CallCheck — шлюз проверки телефона звонком покупателя.
type CallCheck interface {
	// Start заводит проверку и выдаёт номер, на который звонить. ip — адрес
	// покупателя: по нему шлюз выбирает номер; пусто — не передаётся.
	Start(ctx context.Context, phone buyer.Phone, ip string) (buyer.CallInstruction, error)
	// Status — состояние проверки у шлюза.
	Status(ctx context.Context, providerRef string) (buyer.CallStatus, error)
}

// RateLimiter — лимиты частоты по ключу в окне времени.
type RateLimiter interface {
	// Allow засчитывает попытку; false — лимит исчерпан, retryAfter — когда окно откроется.
	Allow(ctx context.Context, key string, limit int, window time.Duration) (allowed bool, retryAfter time.Duration, err error)
}

// Leases — аренда фоновых задач между процессами: одну работу делает один процесс.
type Leases interface {
	// Acquire берёт аренду name на ttl для holder; занята другим — false.
	Acquire(ctx context.Context, name, holder string, ttl time.Duration) (bool, error)
	// Release отпускает аренду holder.
	Release(ctx context.Context, name, holder string) error
}
