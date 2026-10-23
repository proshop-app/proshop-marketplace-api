// Package catalog — сценарии каталога: приём проекций из событий магазинов,
// синхронизация индекса предложений и запросы выдачи.
package catalog

import (
	"encoding/json"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/pkg/errs"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

const (
	// TaskSyncOffer — переписать в индексе одно предложение.
	TaskSyncOffer = "catalog.offer.sync"
	// TaskSyncTenant — переписать в индексе все предложения магазина.
	TaskSyncTenant = "catalog.tenant.sync"
)

// ErrInvalidTask — задача outbox повреждена.
var ErrInvalidTask = errs.New(errs.Invalid, "catalog.invalid_task", "некорректная задача синхронизации индекса")

type taskPayload struct {
	TenantID  string `json:"tenantId"`
	ProductID string `json:"productId,omitempty"`
}

// SyncOfferTask — задача синхронизации одного предложения.
func SyncOfferTask(tenantID ids.TenantID, productID ids.ProductID, at time.Time) ports.NewTask {
	payload, _ := json.Marshal(taskPayload{TenantID: string(tenantID), ProductID: string(productID)})

	return ports.NewTask{
		Kind:     TaskSyncOffer,
		TenantID: tenantID,
		DedupKey: TaskSyncOffer + ":" + ids.OfferID{Tenant: tenantID, Product: productID}.String(),
		Payload:  payload,
		At:       at,
	}
}

// SyncTenantTask — задача синхронизации всех предложений магазина.
func SyncTenantTask(tenantID ids.TenantID, at time.Time) ports.NewTask {
	payload, _ := json.Marshal(taskPayload{TenantID: string(tenantID)})

	return ports.NewTask{
		Kind:     TaskSyncTenant,
		TenantID: tenantID,
		DedupKey: TaskSyncTenant + ":" + string(tenantID),
		Payload:  payload,
		At:       at,
	}
}

func decodeTask(task ports.Task) (ids.TenantID, ids.ProductID, error) {
	var payload taskPayload
	if err := json.Unmarshal(task.Payload, &payload); err != nil {
		return "", "", ErrInvalidTask.Wrap(err)
	}

	tenantID, err := ids.ParseTenantID(payload.TenantID)
	if err != nil || tenantID != task.TenantID {
		return "", "", ErrInvalidTask.WithMessage("магазин задачи не совпадает с полезной нагрузкой")
	}
	if task.Kind != TaskSyncOffer {
		return tenantID, "", nil
	}

	productID, err := ids.ParseProductID(payload.ProductID)
	if err != nil {
		return "", "", ErrInvalidTask.Wrap(err)
	}

	return tenantID, productID, nil
}
