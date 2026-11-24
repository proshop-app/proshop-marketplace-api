package config_test

import (
	"strings"
	"testing"

	"github.com/proshop-app/proshop-marketplace/internal/config"
)

func TestKafkaWorkersDefaultAndOverride(t *testing.T) {
	env := validEnv()
	cfg, err := config.Load(envOf(env))
	if err != nil || cfg.Kafka.Workers != 8 || cfg.Kafka.BulkWorkers != 3 {
		t.Fatalf("воркеров всего %d, массовых %d, ошибка %v", cfg.Kafka.Workers, cfg.Kafka.BulkWorkers, err)
	}
	env["KAFKA_WORKERS"] = "3"
	env["KAFKA_BULK_WORKERS"] = "1"
	cfg, err = config.Load(envOf(env))
	if err != nil || cfg.Kafka.Workers != 3 || cfg.Kafka.BulkWorkers != 1 {
		t.Fatalf("воркеров всего %d, массовых %d, ошибка %v", cfg.Kafka.Workers, cfg.Kafka.BulkWorkers, err)
	}
}

func TestKafkaWorkersRejectBrokenSplit(t *testing.T) {
	cases := []struct {
		name    string
		workers string
		bulk    string
	}{
		{"KAFKA_WORKERS", "1", "1"},
		{"KAFKA_BULK_WORKERS", "3", "3"},
		{"KAFKA_BULK_WORKERS", "2", "4"},
	}
	for _, tc := range cases {
		env := validEnv()
		env["KAFKA_WORKERS"] = tc.workers
		env["KAFKA_BULK_WORKERS"] = tc.bulk
		if _, err := config.Load(envOf(env)); err == nil || !strings.Contains(err.Error(), tc.name) {
			t.Fatalf("%s/%s: ожидалась ошибка %s, получено %v", tc.workers, tc.bulk, tc.name, err)
		}
	}
}
