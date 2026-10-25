package config_test

import (
	"encoding/base64"
	"strings"
	"testing"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/config"
)

func envOf(values map[string]string) func(string) (string, bool) {
	return func(name string) (string, bool) {
		value, ok := values[name]

		return value, ok
	}
}

func validEnv() map[string]string {
	return map[string]string{
		"IDENTITY_HUB_URL":            "https://identity.example.test",
		"IDENTITY_OPERATOR_CLIENT_ID": "marketplace-operator-test",
		"IDENTITY_OPERATOR_SECRET":    strings.Repeat("o", 32),
		"IDENTITY_CLIENT_ID":          "marketplace-test",
		"IDENTITY_BACKEND_SECRET":     strings.Repeat("i", 32),
		"IDENTITY_REFRESH_KEY":        base64.StdEncoding.EncodeToString([]byte(strings.Repeat("k", 32))),
		"DATABASE_URL":                "postgres://marketplace:marketplace@localhost:5432/marketplace",
		"OPENSEARCH_URLS":             "http://localhost:9200",
		"KAFKA_BROKERS":               "localhost:9092, localhost:9093",
		"OPERATOR_TOKEN":              strings.Repeat("x", 32),
		"REDIS_URL":                   "redis://localhost:6379/0",
		"MARKETPLACE_PUBLIC_URL":      "https://market.example",
		"PLATFORM_API_URL":            "http://server:3000",
		"SERVICE_TOKEN_MARKETPLACE":   strings.Repeat("s", 32),
		"SMSRU_API_ID":                "key",
	}
}

func TestLoadDefaults(t *testing.T) {
	cfg, err := config.Load(envOf(validEnv()))
	if err != nil {
		t.Fatal(err)
	}

	if cfg.HTTP.Addr != ":8080" || cfg.Kafka.GroupID != "marketplace" || cfg.OpenSearch.Index != "marketplace-offers" {
		t.Fatalf("неожиданные умолчания: %+v", cfg)
	}
	if len(cfg.Kafka.Brokers) != 2 || cfg.Kafka.Brokers[1] != "localhost:9093" {
		t.Fatalf("брокеры: %v", cfg.Kafka.Brokers)
	}
	if cfg.Retention.ProcessedEvents != 14*24*time.Hour || cfg.Outbox.Batch != 100 || cfg.Database.MaxConns != 10 {
		t.Fatalf("неожиданные умолчания: %+v", cfg)
	}
}

func TestLoadReportsAllMissing(t *testing.T) {
	_, err := config.Load(envOf(map[string]string{}))
	if err == nil {
		t.Fatal("ожидалась ошибка")
	}

	required := []string{
		"DATABASE_URL",
		"OPENSEARCH_URLS",
		"KAFKA_BROKERS",
		"OPERATOR_TOKEN",
		"REDIS_URL",
		"PLATFORM_API_URL",
		"SERVICE_TOKEN_MARKETPLACE",
		"SMSRU_API_ID",
	}

	for _, name := range required {
		if !strings.Contains(err.Error(), name) {
			t.Fatalf("в ошибке нет %s: %v", name, err)
		}
	}
}

func TestLoadWeekTwo(t *testing.T) {
	cfg, err := config.Load(envOf(validEnv()))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.SmsRu.Mode != config.SmsRuLive ||
		cfg.Login.PhoneLimit.Count != 3 ||
		cfg.Login.PhoneLimit.Window != 10*time.Minute ||
		cfg.Cart.TTL != 30*24*time.Hour ||
		cfg.Delivery.PickupIndex != "marketplace-pickup-points" ||
		cfg.Delivery.CDEK.ClientID != "" {
		t.Fatalf("умолчания недели 2: %+v", cfg)
	}

	env := validEnv()
	env["SMSRU_MODE"] = "sandbox"
	delete(env, "SMSRU_API_ID")
	env["LOGIN_IP_LIMIT"] = "50/30m"
	env["CDEK_CLIENT_ID"] = "id"
	env["CDEK_CLIENT_SECRET"] = "secret"

	cfg, err = config.Load(envOf(env))
	if err != nil || cfg.SmsRu.Mode != config.SmsRuSandbox || cfg.Login.IPLimit.Count != 50 ||
		cfg.Delivery.CDEK.ClientSecret != "secret" {
		t.Fatalf("песочница без ключа и свои лимиты: %+v %v", cfg, err)
	}

	bad := validEnv()
	bad["LOGIN_PHONE_LIMIT"] = "three"
	bad["REDIS_URL"] = "http://x"
	bad["PLATFORM_API_URL"] = "server"
	bad["SERVICE_TOKEN_MARKETPLACE"] = "short"
	bad["CDEK_CLIENT_ID"] = "only-id"
	bad["SMSRU_MODE"] = "fake"

	_, err = config.Load(envOf(bad))
	invalid := []string{
		"LOGIN_PHONE_LIMIT",
		"REDIS_URL",
		"PLATFORM_API_URL",
		"SERVICE_TOKEN_MARKETPLACE",
		"CDEK_CLIENT_ID",
		"SMSRU_MODE",
	}

	for _, name := range invalid {
		if err == nil || !strings.Contains(err.Error(), name) {
			t.Fatalf("в ошибке нет %s: %v", name, err)
		}
	}
}

func TestLoadValidatesValues(t *testing.T) {
	env := validEnv()
	env["OPERATOR_TOKEN"] = "short"
	env["DATABASE_URL"] = "mysql://x"
	env["OPENSEARCH_URLS"] = "localhost:9200"
	env["OUTBOX_BATCH"] = "zero"
	env["OUTBOX_LEASE"] = "-1s"
	env["OPENSEARCH_USERNAME"] = "admin"
	env["LOG_LEVEL"] = "verbose"
	env["KAFKA_MAX_ATTEMPTS"] = "0"

	_, err := config.Load(envOf(env))
	if err == nil {
		t.Fatal("ожидалась ошибка")
	}

	invalid := []string{
		"OPERATOR_TOKEN",
		"DATABASE_URL",
		"OPENSEARCH_URLS",
		"OUTBOX_BATCH",
		"OUTBOX_LEASE",
		"OPENSEARCH_USERNAME",
		"LOG_LEVEL",
		"KAFKA_MAX_ATTEMPTS",
	}

	for _, name := range invalid {
		if !strings.Contains(err.Error(), name) {
			t.Fatalf("в ошибке нет %s: %v", name, err)
		}
	}
}

func TestLoadPerTenantNotAboveBatch(t *testing.T) {
	env := validEnv()
	env["OUTBOX_BATCH"] = "10"
	env["OUTBOX_PER_TENANT"] = "11"

	if _, err := config.Load(envOf(env)); err == nil || !strings.Contains(err.Error(), "OUTBOX_PER_TENANT") {
		t.Fatalf("ожидалась ошибка OUTBOX_PER_TENANT, получено %v", err)
	}
}

func TestLoadDatabase(t *testing.T) {
	db, err := config.LoadDatabase(envOf(map[string]string{"DATABASE_URL": "postgresql://u:p@h/db"}))
	if err != nil || db.URL == "" {
		t.Fatalf("%+v, %v", db, err)
	}

	if _, err := config.LoadDatabase(envOf(map[string]string{})); err == nil {
		t.Fatal("ожидалась ошибка без DATABASE_URL")
	}
}

func TestDeliverySubsidyMinPercent(t *testing.T) {
	cfg, err := config.Load(envOf(validEnv()))
	if err != nil || cfg.Checkout.DeliverySubsidyMinPercent != 30 {
		t.Fatalf("умолчание: %+v %v", cfg.Checkout, err)
	}
	for _, tc := range []struct {
		raw   string
		want  int
		valid bool
	}{
		{"0", 0, true},
		{"45", 45, true},
		{"100", 100, true},
		{"-1", 0, false},
		{"101", 0, false},
		{"30.5", 0, false},
	} {
		t.Run(tc.raw, func(t *testing.T) {
			env := validEnv()
			env["DELIVERY_SUBSIDY_MIN_PERCENT"] = tc.raw
			cfg, err := config.Load(envOf(env))
			if tc.valid {
				if err != nil || cfg.Checkout.DeliverySubsidyMinPercent != tc.want {
					t.Fatalf("порог: %+v %v", cfg.Checkout, err)
				}
			} else if err == nil || !strings.Contains(err.Error(), "DELIVERY_SUBSIDY_MIN_PERCENT") {
				t.Fatalf("ожидалась ошибка порога: %v", err)
			}
		})
	}
}

func TestOperatorAndRegionConfiguration(t *testing.T) {
	values := validEnv()
	values["OPERATOR_EMAIL"] = "op@example.test"
	if _, err := config.Load(envOf(values)); err == nil {
		t.Fatal("неполный вход")
	}
	values["OPERATOR_PASSWORD_HASH"] = "hash-validated-by-startup"
	values["OPERATOR_ORIGIN"] = "https://admin.example"
	values["CDEK_REGION_CODES"] = `{"81":"RU-MOW"}`
	cfg, err := config.Load(envOf(values))
	if err != nil || cfg.Operator.SessionTTL != 8*time.Hour || cfg.Delivery.CDEK.RegionCodes["81"] != "RU-MOW" {
		t.Fatal(cfg.Operator, err)
	}
	for name, value := range map[string]string{
		"OPERATOR_ORIGIN":      "https://admin.example/path",
		"OPERATOR_SESSION_TTL": "25h",
		"CDEK_REGION_CODES":    `{"81":"fake:abc"}`,
	} {
		saved := values[name]
		values[name] = value
		if _, err := config.Load(envOf(values)); err == nil {
			t.Fatal(name, value)
		}
		values[name] = saved
	}
}
