package config_test

import (
	"strings"
	"testing"

	"github.com/proshop-app/proshop-marketplace/internal/config"
)

func TestSearchFeedbackRequiresDistinctSecrets(t *testing.T) {
	for _, name := range []string{"SEARCH_ANALYTICS_KEY", "SEARCH_MARKETPLACE_SEARCH_PURCHASE_TOKEN"} {
		t.Run(name, func(t *testing.T) {
			values := validEnv()
			for _, invalid := range []string{"short", values["SEARCH_MARKETPLACE_TOKEN"]} {
				values[name] = invalid
				if _, err := config.Load(envOf(values)); err == nil || !strings.Contains(err.Error(), name) {
					t.Fatal("ожидалась ошибка настройки", err)
				}
			}
		})
	}
}

func TestSearchFeedbackMissingSecretsAllowStartup(t *testing.T) {
	for _, missing := range [][]string{
		{"SEARCH_ANALYTICS_KEY"},
		{"SEARCH_MARKETPLACE_SEARCH_PURCHASE_TOKEN"},
		{"SEARCH_ANALYTICS_KEY", "SEARCH_MARKETPLACE_SEARCH_PURCHASE_TOKEN"},
	} {
		t.Run(strings.Join(missing, "+"), func(t *testing.T) {
			values := validEnv()
			for _, key := range missing {
				delete(values, key)
			}
			cfg, err := config.Load(envOf(values))
			if err != nil || cfg.Search.FeedbackEnabled() {
				t.Fatal("без обоих секретов функция должна быть выключена", err)
			}
		})
	}
}
