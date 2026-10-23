// Команда marketplace — процесс маркетплейса. Граф зависимостей собирается
// руками: конфиг → хранилища → сценарии приложения → транспорт.
//
//	marketplace                        HTTP, консьюмер Kafka, разбор outbox и фоновые задачи
//	marketplace reindex                перестроение индекса предложений из проекции
//	marketplace refresh-pickup-points  обновление справочников пунктов выдачи
package main

import "os"

func main() {
	os.Exit(run())
}
