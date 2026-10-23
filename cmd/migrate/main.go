// Команда migrate — миграции базы маркетплейса из репозитория.
//
//	migrate [up]   применить все
//	migrate down   откатить последнюю
//	migrate status показать состояние
package main

import "os"

func main() {
	os.Exit(run())
}
