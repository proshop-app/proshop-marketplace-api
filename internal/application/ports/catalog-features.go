package ports

import (
	"context"
	"time"

	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

// Review содержит только публичные поля опубликованного отзыва.
type Review struct {
	Author    string    `json:"author"`
	Rating    int       `json:"rating"`
	Text      string    `json:"text"`
	CreatedAt time.Time `json:"createdAt"`
}

// ReviewPage — страница отзывов сервера.
type ReviewPage struct {
	Items []Review `json:"items"`
	Total int      `json:"total"`
}

// BuyerInterestSet объединяет категории интереса и уже купленные предложения.
type BuyerInterestSet struct {
	Categories []string
	Purchased  []ids.OfferID
}

// BuyerInterests позволяет подключить избранное независимо от сценария рекомендаций.
type BuyerInterests interface {
	Interests(context.Context, ids.UUID) (BuyerInterestSet, error)
}

// RecommendationFilter отбирает кандидатов до пагинации одним запросом проекции.
type RecommendationFilter struct {
	Categories    []string
	Excluded      []ids.OfferID
	OtherTenant   ids.TenantID
	Region        string
	PriceMin      *int64
	PriceMax      *int64
	PriceContexts map[ids.TenantID]string
	Limit         int
}

// RecommendationOffers возвращает согласованные снимки кандидатов без N+1.
type RecommendationOffers interface {
	Recommend(context.Context, RecommendationFilter) ([]OfferState, error)
}
