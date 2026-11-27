package ports

import (
	"time"

	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

// TileDocumentPrefix отличает служебный ключ плитки от productId в пакете записи.
const TileDocumentPrefix = "~tile~"

// TileKey идентифицирует плитку в пределах продавца.
type TileKey struct {
	TenantID ids.TenantID
	Key      string
}

// TilePage задаёт позицию и область фонового обхода плиток.
type TilePage struct {
	After    TileKey
	TenantID ids.TenantID
	Since    time.Time
	Limit    int
}

// TileMember содержит состояние SKU и признак отдельной записи SKU в индекс.
type TileMember struct {
	State   OfferState
	Changed bool
}

// TileState собирается из одного согласованного снимка PostgreSQL.
type TileState struct {
	Key       TileKey
	Members   []TileMember
	Version   int64
	UpdatedAt time.Time
}

// TileDocument передаёт полный набор публичных вариантов плитки.
type TileDocument struct {
	Key     string
	Members []OfferDocument
}
