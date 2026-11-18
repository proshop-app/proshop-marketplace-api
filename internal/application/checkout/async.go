package checkout

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/checkout"
	"github.com/proshop-app/proshop-marketplace/pkg/errs"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

// Виды задач создания, отмены и сверки независимой группы магазина.
const (
	TaskCreateOrder    = "checkout.create_order"
	TaskCancelOrder    = "checkout.cancel_order"
	TaskPollOrder      = "checkout.poll_order"
	TaskExpireCreation = "checkout.expire_creation"
)

type creationPendingUntil struct{ at time.Time }

var errCreationPending = errs.New(errs.Unavailable, "checkout.creation_pending", "создание ещё не завершено")

type orderTask struct {
	BuyerID    ids.UUID
	CheckoutID ids.UUID
}

func (e *creationPendingUntil) Error() string { return errCreationPending.Error() }
func (e *creationPendingUntil) Unwrap() error { return errCreationPending }

// CreationPending позволяет циклу отличить штатное ожидание от сбоя.
func CreationPending(err error) bool { return errors.Is(err, errCreationPending) }

func enqueueOrderTask(
	ctx context.Context,
	tx ports.Tx,
	kind string,
	state checkout.State,
	group checkout.Group,
	now time.Time,
) error {
	payload, err := json.Marshal(orderTask{BuyerID: state.BuyerID, CheckoutID: state.ID})
	if err != nil {
		return err
	}
	available := now
	if kind == TaskExpireCreation {
		available = group.CreationDeadline
	}
	return tx.Outbox().Enqueue(ctx, ports.NewTask{
		Kind:        kind,
		TenantID:    group.TenantID,
		DedupKey:    kind + ":" + group.ExternalCheckoutID,
		Payload:     payload,
		At:          now,
		AvailableAt: available,
	})
}

// HandleOrderTask — единственный путь создания; после дедлайна POST магазина запрещён.
func (s *Service) HandleOrderTask(ctx context.Context, task ports.Task) error {
	var payload orderTask
	if err := json.Unmarshal(task.Payload, &payload); err != nil {
		return errs.New(errs.Invalid, "checkout.invalid_task", "повреждена задача заказа").Wrap(err)
	}
	switch task.Kind {
	case TaskCancelOrder:
		return s.cancelGroup(ctx, payload.BuyerID, payload.CheckoutID, task.TenantID)
	case TaskPollOrder:
		return s.pollGroup(ctx, payload.BuyerID, payload.CheckoutID, task.TenantID)
	case TaskExpireCreation:
		expire := func(c *checkout.Checkout, now time.Time) error {
			_, err := c.ExpireCreation(now)
			return err
		}
		state, err := s.retrySelection(ctx, payload.BuyerID, payload.CheckoutID, s.enqueueCancellations, expire)
		if err != nil {
			return err
		}
		for _, g := range state.Groups {
			if g.TenantID == task.TenantID && !g.Excluded() && g.OrderID == "" &&
				g.CreationDeadline.After(s.clock.Now()) {
				return &creationPendingUntil{at: g.CreationDeadline}
			}
			if g.TenantID == task.TenantID && g.CreationExpired(s.clock.Now()) {
				return &creationPendingUntil{at: g.NextCreateAt}
			}
		}
		return s.ReturnCancelled(ctx, payload.BuyerID, payload.CheckoutID)
	case TaskCreateOrder:
		if err := s.createGroup(ctx, payload.BuyerID, payload.CheckoutID, task.TenantID); err != nil {
			return err
		}
		return s.ReturnCancelled(ctx, payload.BuyerID, payload.CheckoutID)
	default:
		return errs.New(errs.Invalid, "checkout.invalid_task", "неизвестная задача заказа")
	}
}

func (s *Service) resumePurchase(ctx context.Context, state checkout.State) error {
	enqueue := func(
		ctx context.Context,
		tx ports.Tx,
		c *checkout.Checkout,
	) error {
		for _, g := range c.Groups() {
			if g.PollDue(s.clock.Now()) {
				if err := enqueueOrderTask(ctx, tx, TaskPollOrder, c.State(), g, s.clock.Now()); err != nil {
					return err
				}
			}
		}
		return s.enqueueCancellations(ctx, tx, c)
	}
	_, err := s.updateSelection(ctx, state.BuyerID, state.ID, enqueue, func(c *checkout.Checkout, now time.Time) error {
		return nil
	})
	if err != nil {
		return err
	}
	return s.ReturnCancelled(ctx, state.BuyerID, state.ID)
}

// Cancel фиксирует отказ покупателя и команды магазинам в одной транзакции.
func (s *Service) Cancel(ctx context.Context, buyerID, id ids.UUID) (checkout.State, error) {
	after := func(ctx context.Context, tx ports.Tx, c *checkout.Checkout) error {
		if err := s.returnItems(ctx, tx, c); err != nil {
			return err
		}
		return s.enqueueCancellations(ctx, tx, c)
	}
	return s.retrySelection(ctx, buyerID, id, after, func(c *checkout.Checkout, now time.Time) error {
		_, err := c.Cancel(now)
		return err
	})
}

func (s *Service) returnItems(ctx context.Context, tx ports.Tx, c *checkout.Checkout) error {
	for _, g := range c.Groups() {
		if err := s.returnGroupInTx(ctx, tx, c, g.TenantID); err != nil {
			return err
		}
	}
	return nil
}
