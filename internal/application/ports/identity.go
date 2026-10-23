package ports

import "github.com/proshop-app/proshop-marketplace/pkg/ids"

// IDs — источник идентификаторов и секретов.
type IDs interface {
	// NewUUID — случайный идентификатор сущности.
	NewUUID() ids.UUID
	// NewToken — непрозрачный секрет сессии, не меньше 256 бит.
	NewToken() string
}
