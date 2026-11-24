package config

// kafkaCapacity проверяет раскладку воркеров приёма. Ёмкость пула приёма под
// воркеры проверяет сборка процесса (cmd): она знает размер полосы статусов заказов.
func (r *reader) kafkaCapacity(kafka Kafka) {
	if kafka.Workers < 2 {
		r.fail("KAFKA_WORKERS", "не меньше 2 для основной и массовой групп Kafka")
	}
	if kafka.BulkWorkers >= kafka.Workers {
		r.fail("KAFKA_BULK_WORKERS", "меньше KAFKA_WORKERS: основной группе нужен хотя бы один воркер")
	}
}
