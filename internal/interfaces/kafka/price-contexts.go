package kafka

import (
	"context"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/pricecontexts"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

// TopicPriceContextChanged принадлежит серверу установки.
const TopicPriceContextChanged = "pricing.buyer.context-changed"

// PriceContexts принимает только сброс, никогда не назначает цену из события.
type PriceContexts interface {
	Invalidate(context.Context, pricecontexts.Changed) error
}

type priceContextChanged struct {
	Version  int       `json:"version"`
	TenantID string    `json:"tenantId"`
	BuyerID  string    `json:"buyerId"`
	ClientID string    `json:"clientId"`
	Revision int64     `json:"revision"`
	At       time.Time `json:"at"`
	Reason   string    `json:"reason"`
}

// WithPriceContexts подключает событие сброса к общей доставке и parking.
func (r *Router) WithPriceContexts(contexts PriceContexts) *Router {
	r.handlers[TopicPriceContextChanged] = bind(contexts, priceContextChangedHandler)
	return r
}

func priceContextChangedHandler(ctx context.Context, target PriceContexts, envelope Envelope) error {
	var value priceContextChanged
	if err := decode(envelope.Message, &value); err != nil {
		return err
	}
	keyTenant, keyBuyer, err := projectionKey(envelope.Message)
	if err != nil {
		return err
	}
	if value.TenantID == "" || keyTenant == "" {
		return ErrTenantMissing
	}
	tenant, err := resolveTenant(keyTenant, value.TenantID, tenantHeader(envelope.Message))
	if err != nil {
		return err
	}
	buyer, err := ids.ParseUUID(value.BuyerID)
	if err != nil || keyBuyer != value.BuyerID || value.Version != 1 ||
		value.Revision < 1 || value.At.IsZero() || envelope.EventID == "" {
		return ErrInvalidPayload.WithMessage("неверный контракт изменения ценового контекста")
	}
	switch value.Reason {
	case "group_changed", "terms_changed", "membership_removed", "configuration_changed":
	default:
		return ErrInvalidPayload.WithMessage("неизвестная причина изменения ценового контекста")
	}
	return target.Invalidate(ctx, pricecontexts.Changed{
		EventID:  envelope.EventID,
		BuyerID:  buyer,
		TenantID: tenant,
	})
}
