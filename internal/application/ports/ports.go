// Package ports — границы приложения с внешним миром. Реализации живут в
// инфраструктуре, в тестах приложения — fake-реализации.
package ports

import (
	"context"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/domain/buyer"
	"github.com/proshop-app/proshop-marketplace/internal/domain/cart"
	"github.com/proshop-app/proshop-marketplace/internal/domain/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/domain/checkout"
	"github.com/proshop-app/proshop-marketplace/internal/domain/moderation"
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
	"github.com/proshop-app/proshop-marketplace/internal/domain/promotion"
	"github.com/proshop-app/proshop-marketplace/internal/domain/reference"
	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
)

// Clock — текущее время и ожидание по этим часам: паузы сценариев проверяются тестами без
// настоящего ожидания.
type Clock interface {
	Now() time.Time
	// Wait ждёт d; false — контекст отменён раньше.
	Wait(ctx context.Context, d time.Duration) bool
}

// UnitOfWork — транзакция: всё, что сделано внутри fn, фиксируется вместе или
// не фиксируется вовсе. Ошибка fn откатывает транзакцию и возвращается как есть.
type UnitOfWork interface {
	Do(ctx context.Context, fn func(ctx context.Context, tx Tx) error) error
}

// Tx — хранилища, привязанные к одной транзакции.
type Tx interface {
	OfferIndexWork() OfferIndexWork
	BulkModeration() BulkModeration
	Revalidation() RevalidationStore
	BuyerFeatures() BuyerFeatures
	Promotion() promotion.Repository
	// AfterCommit откладывает наблюдение до успешной фиксации данных.
	AfterCommit(func())
	ModerationVerdicts() moderation.VerdictRepository
	ModerationCases() moderation.CaseRepository
	ModerationDecisions() moderation.DecisionRepository
	ModerationRules() moderation.RulesRepository
	ProjectionValidation() ProjectionValidation
	ParticipantDecisions() participant.DecisionRepository
	Taxonomy() taxonomy.Repository
	Participants() participant.Repository
	Registry() participant.Registry
	Products() catalog.ProductRepository
	Signals() catalog.SignalsRepository
	Offers() OfferStates
	StoreCards() StoreCards
	Outbox() Outbox
	Inbox() Inbox
	Parked() ParkedEvents
	Buyers() buyer.Repository
	Challenges() buyer.ChallengeRepository
	Carts() cart.Repository
	Checkouts() checkout.Repository
	Reference() reference.Repository
	Synonyms() SynonymRepository
}
