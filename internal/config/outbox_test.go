package config_test

import (
	"strings"
	"testing"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/config"
)

func TestOutboxBatchBounds(t *testing.T) {
	for key, value := range map[string]string{
		"OUTBOX_BATCH":         "201",
		"OUTBOX_PER_TENANT":    "201",
		"OUTBOX_POLL_INTERVAL": "61s",
		"OUTBOX_LEASE":         "35s",
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
	if err != nil || cfg.Outbox.Batch != 200 || cfg.Outbox.PerTenant != 200 || cfg.Outbox.PollInterval != time.Second {
		t.Fatal(cfg.Outbox, err)
	}
}
