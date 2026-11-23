package main

import (
	"strings"
	"testing"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/config"
	"github.com/proshop-app/proshop-marketplace/internal/interfaces/kafka"
)

func poolConfig(ingestion int32) config.Config {
	budget := config.DatabaseBudget{Statement: time.Second, Transaction: 2 * time.Second}
	return config.Config{
		Kafka: config.Kafka{Workers: 8, BulkWorkers: 3},
		Database: config.Database{
			Read:       config.DatabasePool{MaxConns: 8, Budget: budget},
			Checkout:   config.DatabasePool{MaxConns: 6, Budget: budget},
			Ingestion:  config.DatabasePool{MaxConns: ingestion, Budget: budget},
			Background: config.DatabasePool{MaxConns: 10, Budget: budget},
		},
	}
}

func TestIngestionPoolFitsAllKafkaWorkers(t *testing.T) {
	pools, err := databasePools(poolConfig(0))
	if err != nil {
		t.Fatal(err)
	}
	if got := pools[ports.DatabaseIngestion].MaxConns; got != int32(8+kafka.OrderWorkers) {
		t.Fatalf("пул приёма по умолчанию вмещает обе группы и полосу статусов заказов: %d", got)
	}
	if pools[ports.DatabaseRead].MaxConns != 8 || pools[ports.DatabaseBackground].Budget.Transaction != 2*time.Second {
		t.Fatalf("пулы классов переносят размер и сроки: %+v", pools)
	}

	_, err = databasePools(poolConfig(int32(8 + kafka.OrderWorkers - 1)))
	if err == nil || !strings.Contains(err.Error(), "DATABASE_INGESTION_MAX_CONNS") {
		t.Fatalf("пул приёма меньше воркеров — отказ старта: %v", err)
	}
}

func TestProcessPoolsHaveCeiling(t *testing.T) {
	cfg := poolConfig(0)
	cfg.Database.Background.MaxConns = 990
	if _, err := databasePools(cfg); err == nil || !strings.Contains(err.Error(), "сумма пулов") {
		t.Fatalf("сумма пулов ограничена: %v", err)
	}
}
