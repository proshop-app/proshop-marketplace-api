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
			for _, invalid := range []string{"", "short", values["SEARCH_MARKETPLACE_TOKEN"]} {
				values[name] = invalid
				if _, err := config.Load(envOf(values)); err == nil || !strings.Contains(err.Error(), name) {
					t.Fatal("ожидалась ошибка настройки", err)
				}
			}
		})
	}
}
