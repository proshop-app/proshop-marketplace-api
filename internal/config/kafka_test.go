package config_test

import (
	"strings"
	"testing"

	"github.com/proshop-app/proshop-marketplace/internal/config"
)

func TestKafkaWorkersDefaultAndOverride(t *testing.T) {
	env := validEnv()
	cfg, err := config.Load(envOf(env))
	if err != nil || cfg.Kafka.Workers != 5 || cfg.Kafka.BulkWorkers != 3 {
		t.Fatalf("воркеров всего %d, массовых %d, ошибка %v", cfg.Kafka.Workers, cfg.Kafka.BulkWorkers, err)
	}
	env["KAFKA_WORKERS"] = "3"
	env["KAFKA_BULK_WORKERS"] = "1"
	cfg, err = config.Load(envOf(env))
	if err != nil || cfg.Kafka.Workers != 3 || cfg.Kafka.BulkWorkers != 1 {
		t.Fatalf("воркеров всего %d, массовых %d, ошибка %v", cfg.Kafka.Workers, cfg.Kafka.BulkWorkers, err)
	}
}

func TestKafkaWorkersReserveDatabaseCapacity(t *testing.T) {
	cases := []struct {
		pool     string
		workers  string
		bulk     string
		want     int
		wantBulk int
	}{
		{"4", "8", "4", 2, 1},
		{"5", "8", "99", 2, 1},
		{"10", "8", "2", 5, 2},
		{"16", "8", "4", 8, 4},
		{"21", "99", "99", 10, 9},
		{"16", "2", "4", 2, 1},
	}
	for _, tc := range cases {
		t.Run(tc.pool+"/"+tc.workers, func(t *testing.T) {
			env := validEnv()
			env["DATABASE_MAX_CONNS"] = tc.pool
			env["KAFKA_WORKERS"] = tc.workers
			env["KAFKA_BULK_WORKERS"] = tc.bulk
			cfg, err := config.Load(envOf(env))
			if err != nil || cfg.Kafka.Workers != tc.want || cfg.Kafka.BulkWorkers != tc.wantBulk {
				t.Fatalf("воркеров всего %d, массовых %d, ошибка %v", cfg.Kafka.Workers, cfg.Kafka.BulkWorkers, err)
			}
		})
	}
}

func TestKafkaWorkersRejectCapacityBelowTwo(t *testing.T) {
	for name, value := range map[string]string{"DATABASE_MAX_CONNS": "3", "KAFKA_WORKERS": "1"} {
		env := validEnv()
		env[name] = value
		if _, err := config.Load(envOf(env)); err == nil || !strings.Contains(err.Error(), name) {
			t.Fatalf("%s=%s: ожидалась ошибка, получено %v", name, value, err)
		}
	}
}
