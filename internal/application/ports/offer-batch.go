package ports

import "time"

// OfferBatchObserver наблюдает фактическую отправку документов через порт индекса.
type OfferBatchObserver interface {
	ObserveBatch(size int, duration time.Duration, err error)
}
