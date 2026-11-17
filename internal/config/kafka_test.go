package config_test

import (
	"testing"

	"github.com/proshop-app/proshop-marketplace/internal/config"
)

func TestKafkaWorkersDefaultAndOverride(t *testing.T) {
	env := validEnv()
	cfg, err := config.Load(envOf(env))
	if err != nil || cfg.Kafka.Workers != 5 {
		t.Fatalf("число воркеров по умолчанию: %d, ошибка %v", cfg.Kafka.Workers, err)
	}
	env["KAFKA_WORKERS"] = "3"
	cfg, err = config.Load(envOf(env))
	if err != nil || cfg.Kafka.Workers != 3 {
		t.Fatalf("заданное число воркеров: %d, ошибка %v", cfg.Kafka.Workers, err)
	}
}

func TestKafkaWorkersReserveDatabaseCapacity(t *testing.T) {
	cases := []struct {
		pool    string
		workers string
		want    int
	}{
		{"2", "8", 1},
		{"3", "8", 1},
		{"10", "8", 5},
		{"16", "8", 8},
		{"20", "99", 10},
		{"16", "1", 1},
	}
	for _, tc := range cases {
		t.Run(tc.pool+"/"+tc.workers, func(t *testing.T) {
			env := validEnv()
			env["DATABASE_MAX_CONNS"] = tc.pool
			env["KAFKA_WORKERS"] = tc.workers
			cfg, err := config.Load(envOf(env))
			if err != nil || cfg.Kafka.Workers != tc.want {
				t.Fatalf("воркеров %d, ожидалось %d, ошибка %v", cfg.Kafka.Workers, tc.want, err)
			}
		})
	}
	env := validEnv()
	env["DATABASE_MAX_CONNS"] = "1"
	if _, err := config.Load(envOf(env)); err == nil {
		t.Fatal("сервер принял пул без места для HTTP")
	}
}
