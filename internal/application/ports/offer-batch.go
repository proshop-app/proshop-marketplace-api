package ports

import (
	"time"

	"github.com/proshop-app/proshop-marketplace/pkg/errs"
)

// ErrOfferContractMismatch помечает повторяемый отказ записи из-за версии контракта.
var ErrOfferContractMismatch = errs.New(
	errs.Unavailable,
	"search.contract_version_mismatch",
	"поиск ожидает другую версию контракта предложений",
)

// OfferBatchObserver наблюдает фактическую отправку документов через порт индекса.
type OfferBatchObserver interface {
	ObserveBatch(size int, duration time.Duration, err error)
}
