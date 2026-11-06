package ports

import (
	"context"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

// StoreCategory — счётчик категории, объединяющий её потомков.
type StoreCategory struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Count int    `json:"count"`
}

// StoreProduct — снимок товара витрины с действующей ценой.
type StoreProduct struct {
	OfferID   string  `json:"offerId"`
	ProductID string  `json:"productId"`
	Name      string  `json:"name"`
	Price     int64   `json:"price"`
	Image     *string `json:"image"`
}

// StoreSummary — статистика из проекции, независимая от поискового индекса.
type StoreSummary struct {
	VisibleOffers  int                      `json:"-"`
	CategoryCounts map[string]StoreCategory `json:"-"`
	Offers         int                      `json:"offers"`
	FromPrice      *int64                   `json:"fromPrice"`
	Categories     []StoreCategory          `json:"categories"`
	Uncategorized  int                      `json:"uncategorized"`
	Showcase       []StoreProduct           `json:"showcase"`
	Hidden         []participant.CardHidden `json:"hidden"`
	Rating         *int                     `json:"rating"`
	Badge          *string                  `json:"badge"`
	Rank           *int                     `json:"rank"`
}

// ShopColor — публикует только рассчитанный сервером цвет карточки.
type ShopColor struct {
	Card string `json:"card"`
}

// ShopHeader — публичный профиль после применения постмодерации.
type ShopHeader struct {
	TenantID   ids.TenantID    `json:"tenantId"`
	Slug       string          `json:"slug"`
	Name       string          `json:"name"`
	Slogan     string          `json:"slogan"`
	Logo       *string         `json:"logo"`
	Initial    string          `json:"initial"`
	Color      ShopColor       `json:"color"`
	Offers     int             `json:"offers"`
	Categories []StoreCategory `json:"categories"`
	IsNew      bool            `json:"isNew"`
	Showcase   []ShopImage     `json:"showcase"`
}

// ShopImage — ограничивает поля товара в списке магазинов.
type ShopImage struct {
	OfferID string  `json:"offerId"`
	Image   *string `json:"image"`
}

// ShopCategory — категория отбора или крупнейший корневой узел магазина.
type ShopCategory struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// ShopCard — публичная карточка готового магазина с предложениями.
type ShopCard struct {
	TenantID       ids.TenantID  `json:"tenantId"`
	Slug           string        `json:"slug"`
	Name           string        `json:"name"`
	Slogan         string        `json:"slogan"`
	Logo           *string       `json:"logo"`
	Initial        string        `json:"initial"`
	Color          ShopColor     `json:"color"`
	Category       *ShopCategory `json:"category"`
	MoreCategories int           `json:"moreCategories"`
	Offers         int           `json:"offers"`
	FromPrice      *int64        `json:"fromPrice"`
	Showcase       []ShopImage   `json:"showcase"`
	Rating         *int          `json:"rating"`
	IsNew          bool          `json:"isNew"`
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
	// ProfileVersion не даёт старому пересчёту перезаписать свежее решение оператора.
	ProfileVersion int64
	Summary        StoreSummary
	Header         ShopHeader
	Card           ShopCard
	Public         bool
}

// StoreCards — хранение производной карточки без чтения каталога на запросе.
type StoreCards interface {
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
