package config

func (r *reader) validatePlatform(cfg Platform) {
	if cfg.ReadTimeout > cfg.Timeout {
		r.fail("PLATFORM_READ_TIMEOUT", "не больше PLATFORM_API_TIMEOUT")
	}
	if cfg.CacheSize > 100000 {
		r.fail("PLATFORM_CACHE_SIZE", "не больше 100000 записей")
	}
	if cfg.MaxConcurrent > 1000 {
		r.fail("PLATFORM_MAX_CONCURRENT", "не больше 1000 обращений")
	}
	if cfg.CheckoutMaxConcurrent+cfg.ReadMaxConcurrent != cfg.MaxConcurrent {
		r.fail("PLATFORM_CHECKOUT_MAX_CONCURRENT", "сумма пулов должна равняться PLATFORM_MAX_CONCURRENT")
	}
	if cfg.TenantMaxConcurrent < 2 {
		r.fail("PLATFORM_TENANT_MAX_CONCURRENT", "не меньше двух мест для независимых классов")
	}
	if cfg.TenantMaxConcurrent > cfg.MaxConcurrent {
		r.fail("PLATFORM_TENANT_MAX_CONCURRENT", "не больше PLATFORM_MAX_CONCURRENT")
	}
	if cfg.QueueSize > 100000 {
		r.fail("PLATFORM_QUEUE_SIZE", "не больше 100000 ожидающих на класс")
	}
	if cfg.TenantQueueSize > cfg.QueueSize {
		r.fail("PLATFORM_TENANT_QUEUE_SIZE", "не больше PLATFORM_QUEUE_SIZE")
	}
	if cfg.TenantStateSize > 100000 {
		r.fail("PLATFORM_TENANT_STATE_SIZE", "не больше 100000 магазинов")
	}
	if cfg.TenantIdleTTL < cfg.OpenInterval {
		r.fail("PLATFORM_TENANT_IDLE_TTL", "не меньше PLATFORM_OPEN_INTERVAL")
	}
}
