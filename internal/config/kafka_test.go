package config_test

import (
	"testing"

	"github.com/proshop-app/proshop-marketplace/internal/config"
)

func TestKafkaWorkersDefaultAndOverride(t *testing.T) {
	env := validEnv()
	cfg, err := config.Load(envOf(env))
	if err != nil || cfg.Kafka.Workers != 8 {
		t.Fatalf("число воркеров по умолчанию: %d, ошибка %v", cfg.Kafka.Workers, err)
	}
	env["KAFKA_WORKERS"] = "3"
	cfg, err = config.Load(envOf(env))
	if err != nil || cfg.Kafka.Workers != 3 {
		t.Fatalf("заданное число воркеров: %d, ошибка %v", cfg.Kafka.Workers, err)
	}
}
