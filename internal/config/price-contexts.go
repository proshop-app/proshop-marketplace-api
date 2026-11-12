package config

import "time"

// PriceContexts — срок подтверждения цен и пределы фоновой персонализации.
type PriceContexts struct {
	TTL           time.Duration
	StaleTTL      time.Duration
	LoadTimeout   time.Duration
	Size          int
	MaxConcurrent int
}

func (r *reader) priceContexts() PriceContexts {
	cfg := PriceContexts{
		TTL:           r.duration("PRICE_CONTEXT_TTL", time.Minute),
		StaleTTL:      r.optionalInterval("PRICE_CONTEXT_STALE_TTL", 0),
		LoadTimeout:   r.duration("PRICE_CONTEXT_LOAD_TIMEOUT", time.Second),
		Size:          r.positive("PRICE_CONTEXT_CACHE_SIZE", 10000),
		MaxConcurrent: r.positive("PRICE_CONTEXT_MAX_CONCURRENT", 8),
	}
	if cfg.TTL > time.Minute {
		r.fail("PRICE_CONTEXT_TTL", "не больше минуты до подключения событий сервера")
	}
	if cfg.StaleTTL > time.Minute-cfg.TTL {
		r.fail("PRICE_CONTEXT_STALE_TTL", "TTL вместе с устареванием не больше минуты")
	}
	if cfg.Size > 100000 {
		r.fail("PRICE_CONTEXT_CACHE_SIZE", "не больше 100000 записей")
	}
	if cfg.MaxConcurrent > 1000 {
		r.fail("PRICE_CONTEXT_MAX_CONCURRENT", "не больше 1000 загрузок")
	}
	if cfg.LoadTimeout > 30*time.Second {
		r.fail("PRICE_CONTEXT_LOAD_TIMEOUT", "не больше 30 секунд")
	}
	return cfg
}
