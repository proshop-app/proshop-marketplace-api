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
	if p.ReadTimeout != 300*time.Millisecond || p.CacheTTL != 30*time.Second || p.StaleTTL != 5*time.Minute {
		t.Fatal(p)
	}
	if p.TenantStateSize != 10000 || p.TenantIdleTTL != 10*time.Minute || p.CacheSize != 1024 {
		t.Fatal(p)
	}
	if p.MaxConcurrent != 128 || p.TenantMaxConcurrent != 8 || p.FailureThreshold != 5 {
		t.Fatal(p)
	}
	if p.CheckoutMaxConcurrent != 96 || p.ReadMaxConcurrent != 32 || p.QueueSize != 1024 || p.TenantQueueSize != 32 {
		t.Fatal(p)
	}
	if p.FailureWindow != 30*time.Second || p.FailureMinCalls != 20 || p.FailurePercent != 50 {
		t.Fatal(p)
	}
	if p.OpenInterval != 5*time.Second || p.LogInterval != 30*time.Second {
		t.Fatal(p)
	}
}

func TestPlatformProtectionValidation(t *testing.T) {
	for name, value := range map[string]string{
		"PLATFORM_READ_TIMEOUT":            "6s",
		"PLATFORM_CACHE_TTL":               "0s",
		"PLATFORM_STALE_TTL":               "-1s",
		"PLATFORM_TENANT_STATE_SIZE":       "100001",
		"PLATFORM_TENANT_IDLE_TTL":         "1s",
		"PLATFORM_CACHE_SIZE":              "100001",
		"PLATFORM_TENANT_MAX_CONCURRENT":   "129",
		"PLATFORM_CHECKOUT_MAX_CONCURRENT": "128",
		"PLATFORM_READ_MAX_CONCURRENT":     "128",
		"PLATFORM_QUEUE_SIZE":              "100001",
		"PLATFORM_TENANT_QUEUE_SIZE":       "1025",
		"PLATFORM_MAX_CONCURRENT":          "1001",
		"PLATFORM_FAILURE_WINDOW":          "301s",
		"PLATFORM_FAILURE_MIN_CALLS":       "1000001",
		"PLATFORM_FAILURE_PERCENT":         "101",
		"PLATFORM_FAILURE_THRESHOLD":       "0",
		"PLATFORM_OPEN_INTERVAL":           "invalid",
		"PLATFORM_LOG_INTERVAL":            "0s",
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

func TestPlatformProtectionConsistency(t *testing.T) {
	for _, values := range []map[string]string{
		{"PLATFORM_MAX_CONCURRENT": "1"},
		{"PLATFORM_FAILURE_WINDOW": "500ms"},
		{"PLATFORM_FAILURE_MIN_CALLS": "0"},
		{"PLATFORM_FAILURE_PERCENT": "0"},
		{"PLATFORM_TENANT_IDLE_TTL": "10s"},
		{"PLATFORM_TENANT_MAX_CONCURRENT": "1"},
		{"PLATFORM_TENANT_STATE_SIZE": "2175"},
		{"PLATFORM_QUEUE_SIZE": "0"},
		{"PLATFORM_TENANT_QUEUE_SIZE": "0"},
		{"PLATFORM_READ_MAX_CONCURRENT": "0"},
		{"PLATFORM_CHECKOUT_MAX_CONCURRENT": "0"},
	} {
		env := validEnv()
		for key, value := range values {
			env[key] = value
		}
		if _, err := config.Load(envOf(env)); err == nil {
			t.Fatal("принята несогласованная защита", values)
		}
	}
}

func TestPlatformSharesFollowConfiguredTotal(t *testing.T) {
	values := validEnv()
	values["PLATFORM_MAX_CONCURRENT"] = "64"
	cfg, err := config.Load(envOf(values))
	if err != nil {
		t.Fatal(err)
	}
	p := cfg.Platform
	if p.CheckoutMaxConcurrent != 48 || p.ReadMaxConcurrent != 16 || p.TenantMaxConcurrent != 4 {
		t.Fatal(p)
	}
	values["PLATFORM_CHECKOUT_MAX_CONCURRENT"] = "32"
	values["PLATFORM_READ_MAX_CONCURRENT"] = "32"
	values["PLATFORM_TENANT_MAX_CONCURRENT"] = "16"
	cfg, err = config.Load(envOf(values))
	if err != nil || cfg.Platform.CheckoutMaxConcurrent != 32 || cfg.Platform.TenantMaxConcurrent != 16 {
		t.Fatal(cfg.Platform, err)
	}
}
