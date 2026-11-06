package config_test

import (
	"strings"
	"testing"

	"github.com/proshop-app/proshop-marketplace/internal/config"
)

func TestPromotionConfiguration(t *testing.T) {
	for _, test := range []struct{ name, value string }{
		{"PROMOTION_TIMEZONE", ""},
		{"PROMOTION_TIMEZONE", "Local"},
		{"PROMOTION_TIMEZONE", "invalid/zone"},
		{"PROMOTION_VISITOR_SALT", "short"},
		{"PROMOTION_SEARCH_RELEVANCE_DEPTH", "9"},
		{"PROMOTION_SEARCH_RELEVANCE_DEPTH", "501"},
		{"SERVER_API_TOKEN", "short"},
		{"SERVER_API_TOKEN", strings.Repeat("x", 32)},
	} {
		env := validEnv()
		env[test.name] = test.value
		_, err := config.Load(envOf(env))
		if err == nil || !strings.Contains(err.Error(), test.name) {
			t.Fatal(test.name, err)
		}
	}
}
