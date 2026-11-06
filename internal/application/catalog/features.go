package catalog

import (
	"context"
	"encoding/json"
	"math"
	"sort"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/catalog"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
	"github.com/proshop-app/proshop-marketplace/pkg/regions"
)

const (
	recommendationCacheCapacity = 512
	recommendationCacheTTL      = time.Minute
	recommendationLoadTimeout   = 5 * time.Second
)

// RecommendationRequest — контекст рекомендации и регион доставки.
type RecommendationRequest struct {
	Context string
	OfferID ids.OfferID
	BuyerID ids.UUID
	Region  string
	Limit   int
}

// Recommendations — карточки с объяснением правила отбора.
type Recommendations struct {
	Items  []OfferCard
	Reason string
}

type recommendationCacheEntry struct {
	states  []ports.OfferState
	expires time.Time
}

// Reviews не раскрывает отзывы скрытого предложения.
func (q *Queries) Reviews(ctx context.Context, id ids.OfferID, limit, offset int) (ports.ReviewPage, error) {
	query, err := normalize(ports.OfferQuery{Limit: limit, Offset: offset})
	if err != nil {
		return ports.ReviewPage{}, err
	}
	doc, found, err := q.index.Get(ctx, id.String())
	if err != nil {
		return ports.ReviewPage{}, err
	}
	if !found || !doc.Visible {
		return ports.ReviewPage{}, catalog.ErrOfferNotFound
	}
	return q.platform.ProductReviews(ctx, id.Tenant, id.Product, query.Limit, query.Offset)
}

// Recommend читает кандидатов пачкой; пустой персональный отбор заменяет популярным.
func (q *Queries) Recommend(ctx context.Context, request RecommendationRequest) (Recommendations, error) {
	result := Recommendations{Items: []OfferCard{}, Reason: "popular"}
	if request.Context != "home" && request.Context != "offer" ||
		(request.Region != "" && !regions.ValidRU(request.Region)) {
		return result, ErrInvalidQuery.WithMessage("неизвестный контекст или регион")
	}
	query, err := normalize(ports.OfferQuery{Limit: request.Limit})
	if err != nil {
		return result, err
	}
	contexts, err := q.priceContexts(ctx, request.BuyerID, "")
	if err != nil {
		return result, err
	}
	filter := ports.RecommendationFilter{
		Limit:         query.Limit,
		Region:        request.Region,
		PriceContexts: contexts,
	}
	if request.Context == "offer" {
		var state ports.OfferState
		err = q.uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
			var readErr error
			state, readErr = tx.Offers().Offer(ctx, request.OfferID.Tenant, request.OfferID.Product)
			return readErr
		})
		if err != nil {
			return result, err
		}
		doc, ok := Document(state)
		if !ok || !doc.Visible {
			return result, catalog.ErrOfferNotFound
		}
		key := priceProfileKey(contexts, request.OfferID.Tenant)
		profile, ok := catalog.SelectPrice(doc.PriceProfiles, key)
		if !ok {
			return result, catalog.ErrOfferNotFound
		}
		price := profile.EffectivePrice.Kopecks()
		low, high := recommendationPriceBounds(price)
		filter.PriceMin, filter.PriceMax = &low, &high
		filter.Categories = []string{doc.CategoryID}
		filter.Excluded = []ids.OfferID{request.OfferID}
		filter.OtherTenant = request.OfferID.Tenant
		result.Reason = "similar"
	} else if request.BuyerID != "" {
		interests, readErr := q.interests.Interests(ctx, request.BuyerID)
		if readErr != nil {
			return result, readErr
		}
		filter.Categories, filter.Excluded = interests.Categories, interests.Purchased
		if len(filter.Categories) > 0 {
			result.Reason = "personal"
		}
	}
	states, err := q.recommendCandidates(ctx, request, filter)
	if err != nil {
		return result, err
	}
	appendRecommendations(&result, states, contexts, filter.OtherTenant)
	if result.Reason == "personal" && len(result.Items) == 0 {
		filter.Categories = nil
		result.Reason = "popular"
		states, err = q.recommendations.Recommend(ctx, filter)
		if err != nil {
			return result, err
		}
		appendRecommendations(&result, states, contexts, "")
	}
	return result, nil
}

func appendRecommendations(
	result *Recommendations,
	states []ports.OfferState,
	contexts map[ids.TenantID]string,
	otherTenant ids.TenantID,
) {
	for _, state := range states {
		doc, ok := Document(state)
		if !ok || !doc.Visible {
			continue
		}
		key := priceProfileKey(contexts, state.Product.State().TenantID)
		profile, ok := catalog.SelectPrice(doc.PriceProfiles, key)
		if !ok {
			continue
		}
		doc.Price, doc.BasePrice = profile.EffectivePrice.Kopecks(), profile.Price.Kopecks()
		result.Items = append(result.Items, OfferCard{Offer: doc})
	}
	if otherTenant != "" {
		sort.SliceStable(result.Items, func(i, j int) bool {
			return result.Items[i].Offer.TenantID != string(otherTenant) &&
				result.Items[j].Offer.TenantID == string(otherTenant)
		})
	}
}

// recommendCandidates кэширует только гостевой отбор; персональные цены никогда не разделяются.
func (q *Queries) recommendCandidates(
	ctx context.Context,
	request RecommendationRequest,
	filter ports.RecommendationFilter,
) ([]ports.OfferState, error) {
	if request.BuyerID != "" {
		return q.recommendations.Recommend(ctx, filter)
	}
	raw, err := json.Marshal(filter)
	if err != nil {
		return nil, err
	}
	key := request.Context + ":" + string(raw)
	value, err, _ := q.recommendationFlight.Do(key, func() (any, error) {
		q.recommendationMu.Lock()
		entry, found := q.recommendationCache[key]
		q.recommendationMu.Unlock()
		if found && q.clock.Now().Before(entry.expires) {
			return entry.states, nil
		}
		// Отмена первого клиента не должна срывать общую загрузку для остальных.
		loadCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), recommendationLoadTimeout)
		defer cancel()
		states, err := q.recommendations.Recommend(loadCtx, filter)
		if err != nil {
			return nil, err
		}
		q.recommendationMu.Lock()
		if len(q.recommendationCache) >= recommendationCacheCapacity {
			q.recommendationCache = nil
		}
		if q.recommendationCache == nil {
			q.recommendationCache = make(map[string]recommendationCacheEntry)
		}
		q.recommendationCache[key] = recommendationCacheEntry{
			states:  states,
			expires: q.clock.Now().Add(recommendationCacheTTL),
		}
		q.recommendationMu.Unlock()
		return states, nil
	})
	if err != nil {
		return nil, err
	}
	return value.([]ports.OfferState), nil
}

func priceProfileKey(contexts map[ids.TenantID]string, tenant ids.TenantID) string {
	if contexts == nil {
		return catalog.DefaultPriceProfile
	}
	return contexts[tenant]
}

// recommendationPriceBounds — ceil(0.6*p), floor(1.4*p) без переполнения.
func recommendationPriceBounds(price int64) (int64, int64) {
	low := price/5*3 + (price%5*3+4)/5
	high := int64(math.MaxInt64)
	extra := price/5*2 + price%5*2/5
	if price <= math.MaxInt64-extra {
		high = price + extra
	}
	return low, high
}
