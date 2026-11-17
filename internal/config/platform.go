package config

import "time"

func (r *reader) platform() Platform {
	total := r.positive("PLATFORM_MAX_CONCURRENT", 128)
	// Пулы равны: расчёт для пика утверждённого профиля — docs/platform-dependency.md, «Профиль на 1000 магазинов».
	reads := r.positive("PLATFORM_READ_MAX_CONCURRENT", max(1, total/2))
	return Platform{
		URL:                   r.httpURL("PLATFORM_API_URL"),
		Token:                 r.required("SERVICE_TOKEN_MARKETPLACE"),
		Timeout:               r.duration("PLATFORM_API_TIMEOUT", 5*time.Second),
		ReadTimeout:           r.duration("PLATFORM_READ_TIMEOUT", 300*time.Millisecond),
		CacheTTL:              r.duration("PLATFORM_CACHE_TTL", 30*time.Second),
		StaleTTL:              r.duration("PLATFORM_STALE_TTL", 5*time.Minute),
		CacheSize:             r.positive("PLATFORM_CACHE_SIZE", 33000),
		MaxConcurrent:         total,
		TenantMaxConcurrent:   r.positive("PLATFORM_TENANT_MAX_CONCURRENT", max(2, total/16)),
		CheckoutMaxConcurrent: r.positive("PLATFORM_CHECKOUT_MAX_CONCURRENT", max(1, total-reads)),
		ReadMaxConcurrent:     reads,
		QueueSize:             r.positive("PLATFORM_QUEUE_SIZE", 1024),
		TenantQueueSize:       r.positive("PLATFORM_TENANT_QUEUE_SIZE", 32),
		TenantStateSize:       r.positive("PLATFORM_TENANT_STATE_SIZE", 10000),
		TenantIdleTTL:         r.duration("PLATFORM_TENANT_IDLE_TTL", 10*time.Minute),
		FailureThreshold:      r.positive("PLATFORM_FAILURE_THRESHOLD", 5),
		FailureWindow:         r.duration("PLATFORM_FAILURE_WINDOW", 30*time.Second),
		FailureMinCalls:       r.positive("PLATFORM_FAILURE_MIN_CALLS", 20),
		FailurePercent:        r.positive("PLATFORM_FAILURE_PERCENT", 50),
		OpenInterval:          r.duration("PLATFORM_OPEN_INTERVAL", 5*time.Second),
		LogInterval:           r.duration("PLATFORM_LOG_INTERVAL", 30*time.Second),
	}
}

func (r *reader) validatePlatform(cfg Platform) {
	if cfg.ReadTimeout > cfg.Timeout {
		r.fail("PLATFORM_READ_TIMEOUT", "не больше PLATFORM_API_TIMEOUT")
	}
	if cfg.CacheSize > 100000 {
		r.fail("PLATFORM_CACHE_SIZE", "не больше 100000 записей")
	}
	if cfg.TenantIdleTTL < max(cfg.OpenInterval, cfg.FailureWindow) {
		r.fail("PLATFORM_TENANT_IDLE_TTL", "не меньше PLATFORM_OPEN_INTERVAL и PLATFORM_FAILURE_WINDOW")
	}
	r.validatePlatformBreaker(cfg)
	r.validatePlatformSlots(cfg)
	r.validatePlatformQueues(cfg)
}

func (r *reader) validatePlatformSlots(cfg Platform) {
	if cfg.MaxConcurrent < 2 || cfg.MaxConcurrent > 1000 {
		r.fail("PLATFORM_MAX_CONCURRENT", "от 2 до 1000 обращений")
	}
	sumMatches := cfg.CheckoutMaxConcurrent == cfg.MaxConcurrent-cfg.ReadMaxConcurrent
	if cfg.ReadMaxConcurrent > cfg.MaxConcurrent || !sumMatches {
		message := "сумма с PLATFORM_CHECKOUT_MAX_CONCURRENT должна равняться PLATFORM_MAX_CONCURRENT"
		r.fail("PLATFORM_READ_MAX_CONCURRENT", message)
	}
	if cfg.TenantMaxConcurrent < 2 || cfg.TenantMaxConcurrent > cfg.MaxConcurrent {
		r.fail("PLATFORM_TENANT_MAX_CONCURRENT", "от 2 до PLATFORM_MAX_CONCURRENT для независимых классов")
	}
}

func (r *reader) validatePlatformQueues(cfg Platform) {
	if cfg.QueueSize > 100000 {
		r.fail("PLATFORM_QUEUE_SIZE", "не больше 100000 ожидающих на класс")
	}
	if cfg.TenantQueueSize > cfg.QueueSize {
		r.fail("PLATFORM_TENANT_QUEUE_SIZE", "не больше PLATFORM_QUEUE_SIZE")
	}
	if cfg.TenantStateSize > 100000 {
		r.fail("PLATFORM_TENANT_STATE_SIZE", "не больше 100000 магазинов")
	}
	// Любое сочетание активных и ожидающих магазинов помещается без вытеснения закреплённых состояний.
	if cfg.QueueSize > (cfg.TenantStateSize-cfg.MaxConcurrent)/2 {
		r.fail("PLATFORM_TENANT_STATE_SIZE", "должен вмещать PLATFORM_MAX_CONCURRENT и две PLATFORM_QUEUE_SIZE")
	}
}

func (r *reader) validatePlatformBreaker(cfg Platform) {
	if cfg.FailureWindow < time.Second || cfg.FailureWindow > 5*time.Minute {
		r.fail("PLATFORM_FAILURE_WINDOW", "от 1 секунды до 5 минут")
	}
	if cfg.FailureMinCalls > 1000000 {
		r.fail("PLATFORM_FAILURE_MIN_CALLS", "не больше 1000000 завершённых вызовов")
	}
	if cfg.FailurePercent > 100 {
		r.fail("PLATFORM_FAILURE_PERCENT", "от 1 до 100 процентов")
	}
}
