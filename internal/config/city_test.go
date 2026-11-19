package config_test

import (
	"strings"
	"testing"

	"github.com/proshop-app/proshop-marketplace/internal/config"
)

func TestCityDevelopmentOverrideRequiresExplicitDevelopmentModeAndPublicIP(t *testing.T) {
	for _, mode := range []string{"", "production", "development"} {
		env := validEnv()
		env["MARKETPLACE_CITY_DEV_CLIENT_IP"] = "77.88.8.8"
		if mode != "" {
			env["MARKETPLACE_ENV"] = mode
		}
		cfg, err := config.Load(envOf(env))
		if mode == "development" {
			if err != nil || cfg.City.DevClientIP.String() != "77.88.8.8" {
				t.Fatal(cfg.City, err)
			}
		} else if err == nil || !strings.Contains(err.Error(), "MARKETPLACE_CITY_DEV_CLIENT_IP") {
			t.Fatal(mode, err)
		}
	}
	for _, value := range []string{"127.0.0.1", "100.64.0.1", "::1", "invalid"} {
		env := validEnv()
		env["MARKETPLACE_ENV"] = "development"
		env["MARKETPLACE_CITY_DEV_CLIENT_IP"] = value
		if _, err := config.Load(envOf(env)); err == nil {
			t.Fatal(value)
		}
	}
}
