package config

// Один обработчик держит одно соединение. Половины пула должно хватать на обе группы.
func (r *reader) kafkaCapacity(cfg *Config) {
	if cfg.Database.MaxConns < 4 {
		r.fail("DATABASE_MAX_CONNS", "не меньше 4 для совместной работы HTTP и двух групп Kafka")
	}
	if cfg.Kafka.Workers == 1 {
		r.fail("KAFKA_WORKERS", "не меньше 2 для двух групп Kafka")
	}

	cfg.Kafka.Workers = min(cfg.Kafka.Workers, int(cfg.Database.MaxConns/2))
	if cfg.Kafka.Workers >= 2 {
		cfg.Kafka.BulkWorkers = min(cfg.Kafka.BulkWorkers, cfg.Kafka.Workers-1)
	}
}
