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
	if cfg.TenantStateSize > 100000 {
		r.fail("PLATFORM_TENANT_STATE_SIZE", "не больше 100000 магазинов")
	}
	if cfg.TenantIdleTTL < cfg.OpenInterval {
		r.fail("PLATFORM_TENANT_IDLE_TTL", "не меньше PLATFORM_OPEN_INTERVAL")
	}
}
