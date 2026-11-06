package ports

import "github.com/proshop-app/proshop-marketplace/internal/domain/promotion"

// PromotionMetrics вызываются после фиксации, чтобы откат не увеличивал счётчики.
type PromotionMetrics interface {
	PromotionEvent(promotion.CampaignState, string)
	PromotionTransition(promotion.CampaignState, promotion.Status)
}
