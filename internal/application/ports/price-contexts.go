package ports

import (
	"context"
	"time"

	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

// CachedCatalogPricing читает снимок до обхода магазинов; nil возвращает весь кэш покупателя.
// Вызывающий проверяет текущую видимость и полноту снимка до показа личных цен.
type CachedCatalogPricing interface {
	CatalogPricing
	PriceContextPreloader
}

// PriceContextEntry хранит только выбор профилей, без персональных данных покупателя.
type PriceContextEntry struct {
	Profiles map[ids.TenantID]string `json:"profiles"`
	LoadedAt time.Time               `json:"loadedAt"`
}

// PriceContextCache ограничивает жизнь записи; токен загрузки защищает от записи после сброса.
type PriceContextCache interface {
	Get(context.Context, ids.UUID) (PriceContextEntry, bool, error)
	// Begin атомарно назначает токен и возвращает снимок после всех предшествующих сбросов.
	Begin(context.Context, ids.UUID, string, time.Duration) (PriceContextEntry, error)
	Store(context.Context, ids.UUID, string, PriceContextEntry, time.Duration) (bool, error)
	Delete(context.Context, ids.UUID) error
}

// PriceContextPreloader запускает предзагрузку без ожидания внешнего сервиса.
type PriceContextPreloader interface {
	Preload(ids.UUID)
}

// PriceContextMetrics использует только ограниченный набор исходов, без меток покупателя.
type PriceContextMetrics interface {
	PriceContext(operation, outcome string, elapsed time.Duration)
	PriceContextSize(backend string, size int)
	PriceContextInvalidation(tenant ids.TenantID, outcome string)
}
