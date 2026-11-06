package fake

import (
	"context"
	"sync"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

// CatalogFeatures фиксирует запрос кандидатов для проверки отсутствия N+1.
type CatalogFeatures struct {
	mu             sync.Mutex
	RecommendHook  func(context.Context) error
	CategoryStates map[string][]ports.OfferState
	BuyerInterests ports.BuyerInterestSet
	States         []ports.OfferState
	Filter         ports.RecommendationFilter
	Calls          int
	Err            error
}

// Interests возвращает заданные интересы без обращения к инфраструктуре.
func (f *CatalogFeatures) Interests(context.Context, ids.UUID) (ports.BuyerInterestSet, error) {
	return f.BuyerInterests, f.Err
}

// Recommend сохраняет фильтр и число запросов кандидатов.
func (f *CatalogFeatures) Recommend(
	ctx context.Context,
	filter ports.RecommendationFilter,
) ([]ports.OfferState, error) {
	f.mu.Lock()
	f.Filter = filter
	f.Calls++
	f.mu.Unlock()
	if f.RecommendHook != nil {
		if err := f.RecommendHook(ctx); err != nil {
			return nil, err
		}
	}
	if f.CategoryStates != nil && len(filter.Categories) > 0 {
		return f.CategoryStates[filter.Categories[0]], f.Err
	}
	return f.States, f.Err
}

// ProductReviews возвращает страницу публичных отзывов из памяти.
func (p *Platform) ProductReviews(
	_ context.Context,
	_ ids.TenantID,
	_ ids.ProductID,
	limit, offset int,
) (ports.ReviewPage, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.ReviewsErr != nil {
		return ports.ReviewPage{}, p.ReviewsErr
	}
	result := ports.ReviewPage{Items: []ports.Review{}, Total: len(p.Reviews)}
	if offset < len(p.Reviews) {
		result.Items = append(result.Items, p.Reviews[offset:min(offset+limit, len(p.Reviews))]...)
	}
	return result, nil
}
