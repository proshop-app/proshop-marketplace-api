package config_test

import (
	"strings"
	"testing"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/config"
)

func TestPriceContextDefaultsAndValidation(t *testing.T) {
	cfg, err := config.Load(envOf(validEnv()))
	if err != nil {
		t.Fatal(err)
	}
	p := cfg.PriceContexts
	if p.TTL != time.Minute || p.StaleTTL != 0 || p.Size != 10000 || p.MaxConcurrent != 8 {
		t.Fatal(p)
	}
	for name, value := range map[string]string{
		"PRICE_CONTEXT_TTL":            "61s",
		"PRICE_CONTEXT_STALE_TTL":      "1s",
		"PRICE_CONTEXT_CACHE_SIZE":     "100001",
		"PRICE_CONTEXT_MAX_CONCURRENT": "0",
		"PRICE_CONTEXT_LOAD_TIMEOUT":   "31s",
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
	values := validEnv()
	values["PRICE_CONTEXT_TTL"] = "45s"
	values["PRICE_CONTEXT_STALE_TTL"] = "15s"
	if _, err := config.Load(envOf(values)); err != nil {
		t.Fatal(err)
	}
}
