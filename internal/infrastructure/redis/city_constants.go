package redis

import "time"

// Сроки успешного и пустого ответа повторяют proshop-base/constants/geo.ts.
const (
	cityCacheTTL = 24 * time.Hour
	cityEmptyTTL = 5 * time.Minute
	// Пять секунд подавляют повтор сбоя одним IP, сохраняя быстрый возврат после восстановления.
	cityFailureTTL = 5 * time.Second
	// После 429 ждём полное минутное окно общей квоты источника, включая другие IP и процессы.
	// https://ip-api.com/docs/api:json — Usage limits (45 запросов/минуту с одного исходящего IP).
	cityRateLimitPause = time.Minute
)
