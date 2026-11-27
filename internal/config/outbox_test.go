package config_test

import (
	"strings"
	"testing"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/config"
)

func TestOutboxBatchBounds(t *testing.T) {
	for key, value := range map[string]string{
		"OUTBOX_BATCH":      "201",
		"OUTBOX_PER_TENANT": "201",
	} {
		t.Run(key, func(t *testing.T) {
			env := validEnv()
			env[key] = value
			if _, err := config.Load(envOf(env)); err == nil || !strings.Contains(err.Error(), key) {
				t.Fatalf("некорректная настройка %s: %v", key, err)
			}
		})
	}
	cfg, err := config.Load(envOf(validEnv()))
	if err != nil || cfg.Outbox.Batch != 200 || cfg.Outbox.PerTenant != 200 {
		t.Fatal(cfg.Outbox, err)
	}
}

func TestRetryMetricsIntervalIsConfiguredAndPositive(t *testing.T) {
	env := validEnv()
	env["OUTBOX_RETRY_METRICS_INTERVAL"] = "2m"
	cfg, err := config.Load(envOf(env))
	if err != nil || cfg.Outbox.RetryMetricsInterval != 2*time.Minute {
		t.Fatal(cfg.Outbox, err)
	}
	for _, invalid := range []string{"0s", "-1s", "wrong"} {
		env["OUTBOX_RETRY_METRICS_INTERVAL"] = invalid
		if _, err := config.Load(envOf(env)); err == nil {
			t.Fatal("принят неверный интервал", invalid)
		}
	}
}
