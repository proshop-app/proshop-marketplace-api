package ports

import (
	"context"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/domain/moderation"
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

// BulkModerationInput содержит заблокированные исходные состояния одной страницы.
type BulkModerationInput struct {
	Case      *moderation.Case
	Verdict   *moderation.Verdict
	Revision  int64
	Published bool
}

// BulkModerationChange связывает переход с обязательным журналом решения.
type BulkModerationChange struct {
	Case     *moderation.Case
	Verdict  *moderation.Verdict
	Decision moderation.Decision
}

// CachedBulkApproval сохраняет исходный итог до срока ключа массового решения.
type CachedBulkApproval struct {
	Fingerprint string
	Result      moderation.BulkResult
	ExpiresAt   time.Time
}

// BulkModeration исключает обращения к базе по одному делу.
type BulkModeration interface {
	Cached(context.Context, string) (CachedBulkApproval, bool, error)
	Cache(context.Context, string, CachedBulkApproval) error
	Page(context.Context, moderation.BulkSelector, int) ([]moderation.BulkCase, error)
	Count(context.Context, moderation.BulkSelector) (moderation.BulkCounts, error)
	Read(context.Context, []ids.UUID) (map[ids.UUID]BulkModerationInput, error)
	Members(context.Context, []ids.TenantID) (map[ids.TenantID]*participant.Participant, error)
	Save(context.Context, []BulkModerationChange, []NewTask) error
}
