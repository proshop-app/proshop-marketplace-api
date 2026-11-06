package config

import (
	"time"
	// В scratch нет системной базы IANA, поэтому календарь входит в бинарник.
	_ "time/tzdata"
)

// Promotion хранит настройки календаря и обезличивания посетителей.
type Promotion struct {
	Timezone             string
	SearchRelevanceDepth int
	VisitorSalt          string
	ServerAPIToken       string
}

func (r *reader) promotion() Promotion {
	p := Promotion{
		Timezone:             r.required("PROMOTION_TIMEZONE"),
		SearchRelevanceDepth: r.positive("PROMOTION_SEARCH_RELEVANCE_DEPTH", 100),
		VisitorSalt:          r.required("PROMOTION_VISITOR_SALT"),
		ServerAPIToken:       r.str("SERVER_API_TOKEN", ""),
	}
	if _, err := time.LoadLocation(p.Timezone); err != nil || p.Timezone == "" || p.Timezone == "Local" {
		r.fail("PROMOTION_TIMEZONE", "нужен явный часовой пояс IANA")
	}
	if p.SearchRelevanceDepth < 10 || p.SearchRelevanceDepth > 500 {
		r.fail("PROMOTION_SEARCH_RELEVANCE_DEPTH", "от 10 до 500")
	}
	if len(p.VisitorSalt) < 16 {
		r.fail("PROMOTION_VISITOR_SALT", "не короче 16 символов")
	}
	return p
}

func (r *reader) validatePromotion(c Config) {
	token := c.Promotion.ServerAPIToken
	if token == "" {
		return
	}
	if len(token) < max(minOperatorTokenLength, len(c.Operator.Token)) {
		r.fail("SERVER_API_TOKEN", "не короче OPERATOR_TOKEN и 32 символов")
	}
	others := []string{
		c.Operator.Token,
		c.Platform.Token,
		c.Search.Token,
		c.Search.CandidatesToken,
		c.Reference.ReadToken,
		c.Identity.Secret,
		c.Identity.OperatorSecret,
		c.Delivery.Yandex.Token,
	}
	for _, other := range others {
		if token == other {
			r.fail("SERVER_API_TOKEN", "должен отличаться от остальных служебных токенов")
		}
	}
}
