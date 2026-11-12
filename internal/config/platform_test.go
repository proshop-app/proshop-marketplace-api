package config_test

import (
	"strings"
	"testing"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/config"
)

func TestPlatformProtectionDefaults(t *testing.T) {
	cfg, err := config.Load(envOf(validEnv()))
	if err != nil {
		t.Fatal(err)
	}
	p := cfg.Platform
	if p.ReadTimeout != 300*time.Millisecond || p.CacheTTL != 30*time.Second || p.StaleTTL != 5*time.Minute ||
		p.CacheSize != 1024 || p.MaxConcurrent != 16 || p.FailureThreshold != 5 ||
		p.OpenInterval != 5*time.Second || p.LogInterval != 30*time.Second {
		t.Fatal(p)
	}
}

func TestPlatformProtectionValidation(t *testing.T) {
	for name, value := range map[string]string{
		"PLATFORM_READ_TIMEOUT":      "6s",
		"PLATFORM_CACHE_TTL":         "0s",
		"PLATFORM_STALE_TTL":         "-1s",
		"PLATFORM_CACHE_SIZE":        "100001",
		"PLATFORM_MAX_CONCURRENT":    "1001",
		"PLATFORM_FAILURE_THRESHOLD": "0",
		"PLATFORM_OPEN_INTERVAL":     "invalid",
		"PLATFORM_LOG_INTERVAL":      "0s",
	} {
		t.Run(name, func(t *testing.T) {
			values := validEnv()
			values[name] = value
			_, err := config.Load(envOf(values))
			if err == nil || !strings.Contains(err.Error(), name) {
				t.Fatal(err)
			}
		})
	}
}
