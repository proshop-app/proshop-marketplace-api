package config

import "time"

// Moderation передаёт проверенные настройки при сборке графа.
type Moderation struct {
	MetricsInterval time.Duration
	ProbationOffers int
	RequireImage    bool
}

// LoadModeration проверяет настройки для команды таксономии без секретов HTTP и Kafka.
func LoadModeration(env func(string) (string, bool)) (Moderation, error) {
	r := &reader{env: env}
	result := Moderation{
		MetricsInterval: r.duration("MODERATION_METRICS_INTERVAL", time.Minute),
		ProbationOffers: r.positive("MODERATION_PROBATION_OFFERS", 50),
		RequireImage:    r.boolean("MODERATION_REQUIRE_IMAGE", true),
	}
	return result, r.err()
}
