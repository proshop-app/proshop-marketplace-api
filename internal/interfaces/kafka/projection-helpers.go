package kafka

import (
	"encoding/json"
	"time"

	kafkago "github.com/segmentio/kafka-go"

	"github.com/proshop-app/proshop-marketplace/internal/domain/catalog"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

// DecodeProjectionSnapshot применяет штатное преобразование retained wire bytes.
// Источник и SHA доказательства проверяет maintenance CLI до вызова сценария.
func DecodeProjectionSnapshot(raw []byte, at time.Time) (catalog.Snapshot, error) {
	var event productProjection
	if !json.Valid(raw) || at.IsZero() {
		return catalog.Snapshot{}, ErrInvalidPayload
	}
	if err := decode(kafkago.Message{Value: raw}, &event); err != nil {
		return catalog.Snapshot{}, err
	}
	tenant, err := ids.ParseTenantID(event.TenantID)
	if err != nil {
		return catalog.Snapshot{}, ErrInvalidPayload.Wrap(err)
	}
	product, err := ids.ParseProductID(event.ProductID)
	if err != nil || event.Version != projectionVersion || event.Revision < 1 {
		return catalog.Snapshot{}, ErrInvalidPayload
	}
	return projectionSnapshot(event, tenant, product, at.UTC().Truncate(time.Microsecond))
}

func projectionSnapshot(
	event productProjection,
	tenant ids.TenantID,
	product ids.ProductID,
	at time.Time,
) (catalog.Snapshot, error) {
	snapshot := catalog.Snapshot{TenantID: tenant, ProductID: product, Revision: event.Revision, At: at}
	switch event.Operation {
	case "delete":
		if event.Product != nil {
			return snapshot, ErrInvalidPayload.WithMessage("удаление со снимком товара")
		}
		snapshot.Deleted = true
	case "upsert":
		if event.Product == nil || event.Product.ID != event.ProductID {
			return snapshot, ErrInvalidPayload.WithMessage("снимок товара не совпадает с конвертом")
		}
		if err := fillSnapshot(&snapshot, event.Product); err != nil {
			return snapshot, err
		}
	default:
		return snapshot, ErrInvalidPayload.WithMessage("неизвестная операция проекции: " + event.Operation)
	}
	return snapshot, nil
}
