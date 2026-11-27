package ports

import (
	"context"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/domain/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

// StoreCategory — счётчик категории, объединяющий её потомков.
type StoreCategory struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Count int    `json:"count"`
}

// ShopCardProduct — публичные сведения о выбранном товаре без цены.
type ShopCardProduct struct {
	ProductID string  `json:"productId"`
	OfferID   string  `json:"offerId"`
	Name      string  `json:"name"`
	Image     *string `json:"image"`
}

// StoreSummary — статистика из проекции, независимая от поискового индекса.
type StoreSummary struct {
	CacheSignature string                   `json:"-"`
	Products       []ShopCardProduct        `json:"products"`
	VisibleOffers  int                      `json:"-"`
	CategoryCounts map[string]StoreCategory `json:"-"`
	Offers         int                      `json:"offers"`
	FromPrice      *int64                   `json:"fromPrice"`
	Categories     []StoreCategory          `json:"categories"`
	Uncategorized  int                      `json:"uncategorized"`
	Hidden         []participant.CardHidden `json:"hidden"`
	Rating         *catalog.Rating          `json:"rating"`
	Badge          *string                  `json:"badge"`
	Rank           *int                     `json:"rank"`
}

// ShopHeader — публичный профиль после применения постмодерации.
type ShopHeader struct {
	Rating     *catalog.Rating `json:"rating"`
	TenantID   ids.TenantID    `json:"tenantId"`
	Slug       string          `json:"slug"`
	Name       string          `json:"name"`
	Logo       *string         `json:"logo"`
	Offers     int             `json:"offers"`
	Categories []StoreCategory `json:"categories"`
	IsNew      bool            `json:"isNew"`
}

// ShopCategory — категория отбора или крупнейший корневой узел магазина.
type ShopCategory struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// ShopCard — публичная карточка готового магазина с предложениями.
type ShopCard struct {
	Products       []ShopCardProduct `json:"products"`
	TenantID       ids.TenantID      `json:"tenantId"`
	Slug           string            `json:"slug"`
	Name           string            `json:"name"`
	Logo           *string           `json:"logo"`
	Mark           *string           `json:"mark"`
	Category       *ShopCategory     `json:"category"`
	MoreCategories int               `json:"moreCategories"`
	Offers         int               `json:"offers"`
	FromPrice      *int64            `json:"fromPrice"`
	Rating         *catalog.Rating   `json:"rating"`
	IsNew          bool              `json:"isNew"`
}

// ShopPage — сохраняет общее число результатов до пагинации.
type ShopPage struct {
	Items  []ShopCard `json:"items"`
	Total  int        `json:"total"`
	Limit  int        `json:"limit"`
	Offset int        `json:"offset"`
}

// ShopDirectoryCard содержит поля для поиска по имени, сортировки и маршрута.
type ShopDirectoryCard struct {
	TenantID ids.TenantID `json:"tenantId"`
	Slug     string       `json:"slug"`
	Name     string       `json:"name"`
	SizeTier int          `json:"sizeTier"`
}

// ShopDirectory возвращает актуальные записи и удалённые магазины после курсора.
type ShopDirectory struct {
	Revision int64               `json:"revision"`
	Items    []ShopDirectoryCard `json:"items"`
	Removed  []ids.TenantID      `json:"removed"`
}

// StoreRecord — готовая сводка и публичный снимок одной ревизии пересчёта.
type StoreRecord struct {
	CacheSignature string
	// ProfileVersion не даёт старому пересчёту перезаписать свежее решение оператора.
	ProfileVersion int64
	Summary        StoreSummary
	Header         ShopHeader
	Card           ShopCard
	Public         bool
	PagePublic     bool
}

// StoreSummaryFacts — итог применения дельт и локальных чтений индексов.
type StoreSummaryFacts struct {
	Offers         int
	VisibleOffers  int
	FromPrice      *int64
	Categories     map[string]int
	Uncategorized  int
	Products       []ShopCardProduct
	Rating         *catalog.Rating
	CacheSignature string
	More           bool
}

// StoreCards — хранение производной карточки без чтения каталога на запросе.
type StoreCards interface {
	// ScheduleChanges склеивает ещё не запланированные дельты в задачи по магазинам.
	ScheduleChanges(context.Context, []ids.OfferID, time.Time) error
	// Rollup применяет порцию изменений под замком магазина. До последней порции возвращает только More.
	Rollup(context.Context, ids.TenantID, bool, []string) (StoreSummaryFacts, error)
	// CacheVersion меняется вместе с публичной видимостью и оформлением, но не продажами.
	CacheVersion(context.Context, ...ids.TenantID) (int64, error)
	// BatchPublic возвращает только публичные карточки выбранных магазинов.
	BatchPublic(context.Context, []ids.TenantID) ([]ShopCard, error)
	// UpdateProfile немедленно применяет постмодерацию к публичному снимку и hidden.
	UpdateProfile(context.Context, *participant.Participant) error
	// Lock сериализует только пересчёты, не блокируя строку участника.
	Lock(context.Context, ids.TenantID) error
	// Hide закрывает страницу и исключает магазин из списков до окончания пересчёта.
	Hide(context.Context, ids.TenantID) error
	// Save атомарно заменяет сводку и счётчики категорий.
	Save(context.Context, StoreRecord) error
	// Get читает сводку; до первого пересчёта возвращает participant.ErrNotFound.
	Get(context.Context, ids.TenantID) (StoreRecord, error)
	// Shop возвращает только публичную шапку по слагу.
	Shop(context.Context, string) (ShopHeader, error)
	// List отбирает публичные магазины с общим числом до пагинации.
	List(context.Context, string, int, int) (ShopPage, error)
	Directory(context.Context, int64) (ShopDirectory, error)
}

// StoreCardCache хранит только сериализованные неперсональные карточки и списки магазинов.
type StoreCardCache interface {
	Get(context.Context, string) ([]byte, bool, error)
	Set(context.Context, string, []byte, time.Duration) error
}

const (
	// TaskSyncOffer — актуализация одного предложения в индексе.
	TaskSyncOffer = "catalog.offer.sync"
	// TaskStoreCard — пересчёт готовой сводки, склеенный по магазину.
	TaskStoreCard = "participant.store-card.refresh"
)

// StoreCardTask ставится вместе с изменением источника сводки.
func StoreCardTask(tenant ids.TenantID, at time.Time) NewTask {
	return NewTask{
		Kind:     TaskStoreCard,
		TenantID: tenant,
		DedupKey: TaskStoreCard + ":" + string(tenant),
		Payload:  []byte("{}"),
		At:       at,
	}
}
