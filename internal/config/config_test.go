package config_test

import (
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
		"DATABASE_URL":    "postgres://marketplace:marketplace@localhost:5432/marketplace",
		"OPENSEARCH_URLS": "http://localhost:9200",
		"KAFKA_BROKERS":   "localhost:9092, localhost:9093",
		"OPERATOR_TOKEN":  strings.Repeat("x", 32),
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

	for _, name := range []string{"DATABASE_URL", "OPENSEARCH_URLS", "KAFKA_BROKERS", "OPERATOR_TOKEN"} {
		if !strings.Contains(err.Error(), name) {
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

	for _, name := range []string{"OPERATOR_TOKEN", "DATABASE_URL", "OPENSEARCH_URLS", "OUTBOX_BATCH", "OUTBOX_LEASE", "OPENSEARCH_USERNAME", "LOG_LEVEL", "KAFKA_MAX_ATTEMPTS"} {
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
