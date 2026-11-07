package ports

import (
	"context"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/domain/buyer"
	"github.com/proshop-app/proshop-marketplace/internal/domain/catalog"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
	"github.com/proshop-app/proshop-marketplace/pkg/money"
)

// SavedKind — закрытый набор независимых списков предложений.
type SavedKind string

// SubscriptionState задаёт требуемое состояние подписки.
type SubscriptionState bool

const (
	// Subscribed — подписка включена.
	Subscribed SubscriptionState = true
	// Unsubscribed — подписка выключена.
	Unsubscribed SubscriptionState = false
	// Favorites — избранные предложения.
	Favorites SavedKind = "favorites"
	// PriceWatches — наблюдения за ценой.
	PriceWatches SavedKind = "price_watches"
)

// SnapshotCategory — категория в сохранённой карточке.
type SnapshotCategory struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	URL  string `json:"url"`
}

// OfferSnapshot хранит только поля карточки, без остатков и служебных данных индекса.
type OfferSnapshot struct {
	UpdatedAt  time.Time          `json:"updatedAt"`
	CategoryID string             `json:"categoryId"`
	ID         string             `json:"id"`
	TenantID   string             `json:"tenantId"`
	ProductID  string             `json:"productId"`
	SellerSlug string             `json:"sellerSlug"`
	SellerName string             `json:"sellerName"`
	Name       string             `json:"name"`
	Image      string             `json:"image"`
	URL        string             `json:"url"`
	Categories []SnapshotCategory `json:"categories"`
	Price      int64              `json:"price"`
	BasePrice  int64              `json:"basePrice"`
}

// SavedOffer — снимок предложения при добавлении с неизменной исходной ценой.
type SavedOffer struct {
	Offer        OfferSnapshot
	PriceAtWatch money.Money
	TargetPrice  *money.Money
}

// BuyerFeatures привязан к транзакции; Lock сериализует изменения списков и проверку лимитов.
type BuyerFeatures interface {
	// WatchTenants возвращает уникальные магазины наблюдений без чтения снимков.
	WatchTenants(context.Context, ids.UUID) ([]ids.TenantID, error)
	Lock(context.Context, ids.UUID) error
	Profile(context.Context, ids.UUID) (*buyer.Profile, error)
	SaveProfile(context.Context, ids.UUID, *buyer.Profile) error
	FavoritesPage(context.Context, ids.UUID, int, int) ([]SavedOffer, int, error)
	PriceDrops(context.Context, ids.UUID, map[ids.TenantID]string) (int, error)
	SavedStats(context.Context, ids.UUID, SavedKind, ids.OfferID) (int, bool, error)
	UpdateTarget(context.Context, ids.UUID, SavedKind, ids.OfferID, *money.Money) error
	Saved(context.Context, ids.UUID) ([]SavedOffer, error)
	PutSaved(context.Context, ids.UUID, SavedKind, SavedOffer) error
	DeleteSaved(context.Context, ids.UUID, SavedKind, string) error
	Subscriptions(context.Context, ids.UUID) ([]ids.TenantID, error)
	Subscribe(context.Context, ids.UUID, ids.TenantID, SubscriptionState) error
	Subscribers(context.Context, ids.TenantID, ids.UUID) (int, bool, error)
}

// GeoIP — определяет регион по адресу без сохранения его в профиле.
type GeoIP interface {
	Locate(context.Context, string) (*buyer.Region, error)
}

// GeoIPCache позволяет не расходовать лимит провайдера при попадании в кэш.
type GeoIPCache interface {
	Cached(context.Context, string) (*buyer.Region, bool)
	LocateUncached(context.Context, string) (*buyer.Region, error)
}

// Snapshot оставляет только публичные поля карточки.
func Snapshot(doc OfferDocument) OfferSnapshot {
	categories := make([]SnapshotCategory, 0, len(doc.Categories))
	for _, category := range doc.Categories {
		categories = append(categories, SnapshotCategory(category))
	}
	return OfferSnapshot{
		UpdatedAt:  doc.UpdatedAt,
		CategoryID: doc.CategoryID,
		ID:         doc.ID,
		TenantID:   doc.TenantID,
		ProductID:  doc.ProductID,
		SellerSlug: doc.SellerSlug,
		SellerName: doc.SellerName,
		Name:       doc.Name,
		Image:      doc.Image,
		URL:        doc.URL,
		Categories: categories,
		Price:      doc.Price,
		BasePrice:  doc.BasePrice,
	}
}

// Document восстанавливает карточку без признаков доступности.
func (s OfferSnapshot) Document() OfferDocument {
	categories := make([]catalog.Category, 0, len(s.Categories))
	for _, category := range s.Categories {
		categories = append(categories, catalog.Category(category))
	}
	return OfferDocument{
		UpdatedAt:  s.UpdatedAt,
		CategoryID: s.CategoryID,
		ID:         s.ID,
		TenantID:   s.TenantID,
		ProductID:  s.ProductID,
		SellerSlug: s.SellerSlug,
		SellerName: s.SellerName,
		Name:       s.Name,
		Image:      s.Image,
		URL:        s.URL,
		Categories: categories,
		Price:      s.Price,
		BasePrice:  s.BasePrice,
	}
}
