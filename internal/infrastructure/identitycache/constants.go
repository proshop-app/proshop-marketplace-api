package identitycache

import "time"

const (
	mappingTTL   = 5 * time.Minute
	mappingLimit = 16_384
)
