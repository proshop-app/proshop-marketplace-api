package config

// Один обработчик держит одно соединение. Не меньше половины пула остаётся
// HTTP и фоновым задачам; KAFKA_WORKERS задаёт только дополнительный верхний предел.
func (r *reader) kafkaCapacity(cfg *Config) {
	if cfg.Database.MaxConns < 2 {
		r.fail("DATABASE_MAX_CONNS", "не меньше 2 для совместной работы HTTP и Kafka")
	}
	cfg.Kafka.Workers = min(cfg.Kafka.Workers, int(cfg.Database.MaxConns/2))
}
