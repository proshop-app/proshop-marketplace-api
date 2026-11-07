package config_test

import (
	"testing"

	"github.com/proshop-app/proshop-marketplace/internal/config"
)

func TestDaDataKeyOptional(t *testing.T) {
	values := validEnv()
	cfg, err := config.Load(envOf(values))
	if err != nil || cfg.DaData.APIKey != "" {
		t.Fatal("ключ не должен быть обязательным", err)
	}
	values["DADATA_API_KEY"] = "test-key"
	values["GEOIP_CACHE_SECRET"] = "01234567890123456789012345678901"
	cfg, err = config.Load(envOf(values))
	if err != nil || cfg.DaData.APIKey != "test-key" || cfg.DaData.CacheSecret != "01234567890123456789012345678901" {
		t.Fatal("ключ окружения не прочитан", err)
	}
}

func TestGeoIPSecretLength(t *testing.T) {
	for _, secret := range []string{"short", "0123456789012345678901234567890"} {
		values := validEnv()
		values["GEOIP_CACHE_SECRET"] = secret
		if _, err := config.Load(envOf(values)); err == nil {
			t.Fatal("короткий секрет принят")
		}
	}
}
