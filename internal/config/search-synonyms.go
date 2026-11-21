package config

import "time"

// Synonyms настраивает общий фоновый проход и опциональный провайдер номинаций.
type Synonyms struct {
	Interval   time.Duration
	ModelURL   string
	ModelToken string
	ModelName  string
}

func (r *reader) synonyms() Synonyms {
	cfg := Synonyms{Interval: r.optionalInterval("SEARCH_SYNONYMS_INTERVAL", 15*time.Minute)}
	if r.boolean("SEARCH_SYNONYMS_MODEL_ENABLED", false) {
		cfg.ModelURL = r.str("SEARCH_SYNONYMS_MODEL_URL", "")
		if cfg.ModelURL != "" {
			cfg.ModelName = r.required("SEARCH_SYNONYMS_MODEL_NAME")
			cfg.ModelToken = r.str("SEARCH_SYNONYMS_MODEL_TOKEN", "")
		}
	}
	return cfg
}
