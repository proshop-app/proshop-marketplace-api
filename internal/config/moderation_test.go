package config_test

import (
	"testing"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/config"
)

func TestModerationConfig(t *testing.T) {
	lookup := func(string) (string, bool) { return "", false }
	defaults, err := config.LoadModeration(lookup)
	if err != nil || defaults.ProbationOffers != 50 || !defaults.RequireImage ||
		defaults.MetricsInterval != time.Minute {
		t.Fatal(defaults, err)
	}
	for _, values := range []map[string]string{
		{"MODERATION_PROBATION_OFFERS": "0"},
		{"MODERATION_PROBATION_OFFERS": "-1"},
		{"MODERATION_PROBATION_OFFERS": "значение"},
		{"MODERATION_REQUIRE_IMAGE": "значение"},
		{"MODERATION_METRICS_INTERVAL": "0s"},
		{"MODERATION_METRICS_INTERVAL": "-1s"},
		{"MODERATION_METRICS_INTERVAL": "значение"},
	} {
		_, err := config.LoadModeration(func(key string) (string, bool) { value, ok := values[key]; return value, ok })
		if err == nil {
			t.Fatal("неверная настройка принята", values)
		}
	}
	values := map[string]string{"MODERATION_PROBATION_OFFERS": "2", "MODERATION_REQUIRE_IMAGE": "false"}
	configured, err := config.LoadModeration(func(key string) (string, bool) {
		value, ok := values[key]
		return value, ok
	})
	if err != nil || configured.ProbationOffers != 2 || configured.RequireImage {
		t.Fatal(configured, err)
	}
}
