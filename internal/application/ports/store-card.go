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

// StoreCardProduct — публичные сведения о выбранном товаре без цены.
type StoreCardProduct struct {
	ProductID string  `json:"productId"`
	OfferID   string  `json:"offerId"`
	Name      string  `json:"name"`
	Image     *string `json:"image"`
}

// StoreSummary — статистика из проекции, независимая от поискового индекса.
type StoreSummary struct {
	CacheSignature   string                   `json:"-"`
	Products         []StoreCardProduct       `json:"products"`
	SelectedProducts []StoreCardProduct       `json:"selectedProducts"`
	VisibleOffers    int                      `json:"-"`
	CategoryCounts   map[string]StoreCategory `json:"-"`
	Offers           int                      `json:"offers"`
	FromPrice        *int64                   `json:"fromPrice"`
	Categories       []StoreCategory          `json:"categories"`
	Uncategorized    int                      `json:"uncategorized"`
	Hidden           []participant.CardHidden `json:"hidden"`
	Rating           *catalog.Rating          `json:"rating"`
	Badge            *string                  `json:"badge"`
	Rank             *int                     `json:"rank"`
}

// ShopHeader — публичный профиль после применения постмодерации.
type ShopHeader struct {
	Rating     *catalog.Rating            `json:"rating"`
	TenantID   ids.TenantID               `json:"tenantId"`
	Slug       string                     `json:"slug"`
	Name       string                     `json:"name"`
	Logo       *string                    `json:"logo"`
	Appearance participant.CardAppearance `json:"appearance"`
	Offers     int                        `json:"offers"`
	Categories []StoreCategory            `json:"categories"`
	IsNew      bool                       `json:"isNew"`
}

// ShopCategory — категория отбора или крупнейший корневой узел магазина.
type ShopCategory struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// ShopCard — публичная карточка готового магазина с предложениями.
type ShopCard struct {
	Products       []StoreCardProduct         `json:"products"`
	TenantID       ids.TenantID               `json:"tenantId"`
	Slug           string                     `json:"slug"`
	Name           string                     `json:"name"`
	Logo           *string                    `json:"logo"`
	Appearance     participant.CardAppearance `json:"appearance"`
	Category       *ShopCategory              `json:"category"`
	MoreCategories int                        `json:"moreCategories"`
	Offers         int                        `json:"offers"`
	FromPrice      *int64                     `json:"fromPrice"`
	Rating         *catalog.Rating            `json:"rating"`
	IsNew          bool                       `json:"isNew"`
}

// ShopPage — сохраняет общее число результатов до пагинации.
type ShopPage struct {
	Items  []ShopCard `json:"items"`
	Total  int        `json:"total"`
	Limit  int        `json:"limit"`
	Offset int        `json:"offset"`
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

// StoreCards — хранение производной карточки без чтения каталога на запросе.
type StoreCards interface {
	// CacheVersion меняется вместе с публичной видимостью и оформлением, но не продажами.
	CacheVersion(context.Context) (int64, error)
	// BatchPublic возвращает только публичные карточки выбранных магазинов.
	BatchPublic(context.Context, []ids.TenantID) ([]ShopCard, error)
	// UpdateProfile немедленно применяет постмодерацию к публичному снимку и hidden.
	UpdateProfile(context.Context, *participant.Participant) error
	// Lock сериализует только пересчёты, не блокируя строку участника.
	Lock(context.Context, ids.TenantID) error
	// Save атомарно заменяет сводку и счётчики категорий.
	Save(context.Context, StoreRecord) error
	// Get читает сводку; до первого пересчёта возвращает participant.ErrNotFound.
	Get(context.Context, ids.TenantID) (StoreRecord, error)
	// Shop возвращает только публичную шапку по слагу.
	Shop(context.Context, string) (ShopHeader, error)
	// List отбирает публичные магазины с общим числом до пагинации.
	List(context.Context, string, int, int) (ShopPage, error)
}

// StoreCardCache хранит только сериализованные неперсональные карточки и списки магазинов.
type StoreCardCache interface {
	Get(context.Context, string) ([]byte, bool, error)
	Set(context.Context, string, []byte, time.Duration) error
}

// TaskStoreCard — пересчёт готовой сводки, склеенный по магазину.
const TaskStoreCard = "participant.store-card.refresh"

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
