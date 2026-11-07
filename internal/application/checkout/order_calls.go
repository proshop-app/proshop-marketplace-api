package checkout

import (
	"context"
	"errors"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/checkout"
	"github.com/proshop-app/proshop-marketplace/pkg/errs"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

func (s *Service) createGroup(ctx context.Context, buyerID, id ids.UUID, tenant ids.TenantID) error {
	token := string(s.ids.NewUUID())
	claimed := false
	state, err := s.update(ctx, buyerID, id, func(c *checkout.Checkout, now time.Time) error {
		var err error
		claimed, err = c.ClaimCreation(tenant, token, now)
		return err
	})
	if errors.Is(err, checkout.ErrConflict) {
		return nil
	}
	if err != nil || !claimed {
		return err
	}
	var group checkout.Group
	for _, g := range state.Groups {
		if g.TenantID == tenant {
			group = g
		}
	}
	request := ports.CreateOrderRequest{
		BuyerID:            buyerID,
		PromoCode:          group.PromoCode,
		ExternalCheckoutID: group.ExternalCheckoutID,
		GroupID:            tenant,
		Carrier:            string(state.Delivery.Carrier),
		PickupPointCode:    state.Delivery.PickupPointCode,
		Contact:            state.Contact,
		DeliveryPrice:      group.DeliveryPrice,
		ReturnURL:          state.ReturnURL,
	}
	for _, line := range group.Lines {
		request.Items = append(request.Items, ports.OrderItem{
			ProductID: line.Offer.Product,
			Quantity:  line.Quantity,
			Price:     line.Price,
			Discounts: line.Discounts,
		})
	}
	// Повтор команды должен вернуть сохранённый заказ, даже если код уже истёк.
	// Сервер сравнивает снимок discounts внутри транзакции создания.
	answer, failed := s.platform.CreateOrder(ctx, tenant, request)
	_, err = s.update(ctx, buyerID, id, func(c *checkout.Checkout, now time.Time) error {
		if failed == nil {
			_, attachErr := c.CompleteCreation(tenant, token, answer, now)
			if !errors.Is(attachErr, checkout.ErrInvalidOrder) {
				return attachErr
			}
			failed = attachErr
		}
		code, terminal, prices := creationFailure(failed)
		reason := code
		if errors.Is(failed, checkout.ErrPromoInvalid) {
			reason = checkout.ErrPromoInvalid.Message()
			if typed, ok := errs.As(failed); ok {
				reason = typed.Message()
			}
		}
		var refusal *ports.OrderRefusal
		if errors.As(failed, &refusal) && refusal.Reason != "" {
			reason = refusal.Reason
		}
		_, err := c.FailCreation(tenant, token, code, reason, terminal, prices, now)
		return err
	})
	return err
}

func creationFailure(err error) (string, checkout.GroupStatus, []checkout.PriceChange) {
	if errors.Is(err, checkout.ErrPromoInvalid) {
		return checkout.ErrPromoInvalid.Key(), checkout.GroupPriceChanged, nil
	}
	var price *ports.PriceChanged
	if errors.As(err, &price) {
		return "PRICE_CHANGED", checkout.GroupPriceChanged, price.Items
	}
	var refusal *ports.OrderRefusal
	if errors.As(err, &refusal) {
		if refusal.Unexpected {
			return refusal.Code, "", nil
		}
		switch refusal.Code {
		case "DELIVERY_UNAVAILABLE", "DELIVERY_CHANGED":
			return refusal.Code, checkout.GroupDeliveryUnavailable, nil
		case "OUT_OF_STOCK":
			return refusal.Code, checkout.GroupCancelled, nil
		case "IDEMPOTENCY_CONFLICT":
			return refusal.Code, checkout.GroupSellerUnreachable, nil
		default:
			return refusal.Code, "", nil
		}
	}
	if errors.Is(err, ports.ErrMarketplaceDisabled) {
		return "MARKETPLACE_DISABLED", "", nil
	}
	return "seller_unreachable", "", nil
}

func (s *Service) pollGroup(ctx context.Context, buyerID, id ids.UUID, tenant ids.TenantID) error {
	token := string(s.ids.NewUUID())
	claimed := false
	state, err := s.update(ctx, buyerID, id, func(c *checkout.Checkout, now time.Time) error {
		var err error
		claimed, err = c.ClaimPoll(tenant, token, now)
		return err
	})
	if errors.Is(err, checkout.ErrConflict) {
		return nil
	}
	if err != nil || !claimed {
		return err
	}
	orderID := ""
	for _, g := range state.Groups {
		if g.TenantID == tenant {
			orderID = g.OrderID
		}
	}
	answer, failed := s.platform.GetOrder(ctx, tenant, orderID)
	_, err = s.update(ctx, buyerID, id, func(c *checkout.Checkout, now time.Time) error {
		if failed == nil {
			interval := paidPoll
			if answer.Status == checkout.GroupAwaitingPayment {
				interval = unpaidPoll
			}
			_, observeErr := c.CompletePoll(tenant, token, answer, now.Add(interval), now)
			if observeErr == nil {
				return nil
			}
			failed = observeErr
		}
		reason := "seller_unreachable"
		var refusal *ports.OrderRefusal
		if errors.As(failed, &refusal) {
			reason = refusal.Code
		}
		if errors.Is(failed, checkout.ErrTransition) || errors.Is(failed, checkout.ErrInvalidOrder) ||
			errors.Is(failed, ports.ErrInvalidOrderStatus) {
			reason = "invalid_status"
		}
		_, err := c.FailPoll(tenant, token, reason, now)
		return err
	})
	return err
}
