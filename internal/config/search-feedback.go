package config

import "slices"

// FeedbackEnabled разрешает наблюдение только при полном наборе секретов.
func (s Search) FeedbackEnabled() bool {
	return s.AnalyticsKey != "" && s.PurchaseToken != ""
}

func (r *reader) searchFeedbackSecrets(cfg Config) {
	search := cfg.Search
	otherTokens := []string{search.Token, search.CandidatesToken, cfg.Operator.Token, cfg.Platform.Token}
	if search.PurchaseToken != "" {
		if len(search.PurchaseToken) < minOperatorTokenLength || slices.Contains(otherTokens, search.PurchaseToken) {
			r.fail("SEARCH_MARKETPLACE_SEARCH_PURCHASE_TOKEN", "нужен отдельный токен не короче 32 байт")
		}
	}
	if search.AnalyticsKey != "" {
		if len(search.AnalyticsKey) < minOperatorTokenLength ||
			slices.Contains(append(otherTokens, search.PurchaseToken), search.AnalyticsKey) {
			r.fail("SEARCH_ANALYTICS_KEY", "нужен отдельный ключ не короче 32 байт")
		}
	}
}
