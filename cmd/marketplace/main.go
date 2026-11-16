// Команда marketplace — процесс маркетплейса. Граф зависимостей собирается
// руками: конфиг → хранилища → сценарии приложения → транспорт.
//
//	marketplace                        HTTP, консьюмер Kafka, разбор outbox и фоновые задачи
//	marketplace reindex                перестроение индекса предложений из проекции
//	marketplace reference import --file <пакет.json>  импорт справочных сущностей
package main

import "os"

func main() {
	os.Exit(run())
}
