package config_test

import (
	"strings"
	"testing"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/config"
)

func TestRequestBudgetsAreConfiguredAndValidated(t *testing.T) {
	for _, name := range []string{"HTTP_REQUEST_TIMEOUT", "HTTP_REFERENCE_IMPORT_TIMEOUT"} {
		env := validEnv()
		env[name] = "3s"
		cfg, err := config.Load(envOf(env))
		if err != nil {
			t.Fatal(err)
		}
		got := cfg.HTTP.RequestTimeout
		if name == "HTTP_REFERENCE_IMPORT_TIMEOUT" {
			got = cfg.HTTP.ReferenceImportTimeout
		}
		if got != 3*time.Second {
			t.Fatal(name, got)
		}
		for _, invalid := range []string{"0s", "-1s", "wrong"} {
			env[name] = invalid
			if _, err := config.Load(envOf(env)); err == nil || !strings.Contains(err.Error(), name) {
				t.Fatal(name, invalid, err)
			}
		}
	}
}
