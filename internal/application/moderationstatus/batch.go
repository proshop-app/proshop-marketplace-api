package moderationstatus

import (
	"context"
	"encoding/json"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/moderation"
	"github.com/proshop-app/proshop-marketplace/pkg/errs"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

// HandleOfferBatch читает один согласованный снимок и отправляет одну пачку брокеру.
// Повреждённая задача, карточка или отказ отдельного сообщения не повторяет успешных соседей.
func (s *Service) HandleOfferBatch(ctx context.Context, tasks []ports.Task) []error {
	failures := make([]error, len(tasks))
	keys, positions := offerTaskKeys(tasks, failures)
	if len(keys) == 0 {
		return failures
	}
	statuses, err := s.readStatuses(ctx, keys)
	if err != nil {
		for _, indexes := range positions {
			for _, n := range indexes {
				failures[n] = err
			}
		}
		return failures
	}
	messages, sent := s.statusMessages(statuses, keys, positions, failures)
	s.publishStatuses(ctx, messages, sent, positions, failures)
	return failures
}

func (s *Service) statusMessages(
	statuses []ports.OfferStatus,
	keys []ids.OfferID,
	positions map[ids.OfferID][]int,
	failures []error,
) ([]ports.Message, []ids.OfferID) {
	byKey := make(map[ids.OfferID]ports.OfferStatus, len(statuses))
	for _, status := range statuses {
		byKey[status.Key] = status
	}
	messages := make([]ports.Message, 0, len(statuses))
	sent := make([]ids.OfferID, 0, len(statuses))
	at := s.clock.Now().UTC().Format(time.RFC3339)
	for _, key := range keys {
		status, found := byKey[key]
		if !found || status.Deleted {
			continue
		}
		if status.Err != nil {
			for _, n := range positions[key] {
				failures[n] = moderation.ErrInvalid.Wrap(status.Err)
			}
			continue
		}
		body, _ := json.Marshal(statusEvent(status, at))
		messageKey, _ := json.Marshal([]string{string(key.Tenant), string(key.Product)})
		messages = append(messages, ports.Message{
			Topic:   s.offerTopic,
			Key:     messageKey,
			Value:   body,
			Headers: map[string]string{"x-tenant": string(key.Tenant)},
		})
		sent = append(sent, key)
	}
	return messages, sent
}

func (s *Service) publishStatuses(
	ctx context.Context,
	messages []ports.Message,
	sent []ids.OfferID,
	positions map[ids.OfferID][]int,
	failures []error,
) {
	if len(messages) > 0 {
		published := s.publisher.PublishBatch(ctx, messages)
		for i, key := range sent {
			var failure error
			if len(published) > 0 {
				if len(published) != len(sent) {
					failure = errs.New(errs.Internal, "moderation.batch_publication", "неполный результат публикации")
				} else {
					failure = published[i]
				}
			}
			for _, n := range positions[key] {
				failures[n] = failure
			}
		}
	}
}

func offerTaskKeys(tasks []ports.Task, failures []error) ([]ids.OfferID, map[ids.OfferID][]int) {
	keys := make([]ids.OfferID, 0, len(tasks))
	positions := map[ids.OfferID][]int{}
	for n, task := range tasks {
		var p payload
		err := json.Unmarshal(task.Payload, &p)
		if err == nil {
			_, err = ids.ParseTenantID(string(p.TenantID))
		}
		if err == nil {
			_, err = ids.ParseProductID(string(p.ProductID))
		}
		if err != nil || task.Kind != TaskOffer || p.TenantID != task.TenantID {
			failures[n] = moderation.ErrInvalid.Wrap(err)
			continue
		}
		key := ids.OfferID{Tenant: p.TenantID, Product: p.ProductID}
		if len(positions[key]) == 0 {
			keys = append(keys, key)
		}
		positions[key] = append(positions[key], n)
	}
	return keys, positions
}

func (s *Service) readStatuses(ctx context.Context, keys []ids.OfferID) ([]ports.OfferStatus, error) {
	var statuses []ports.OfferStatus
	err := s.uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		var err error
		statuses, err = tx.Offers().StatusBatch(ctx, keys)
		return err
	})
	return statuses, err
}

func statusEvent(status ports.OfferStatus, at string) *OfferEvent {
	value, reasons := Status(status.Publication, status.Verdict)
	event := &OfferEvent{
		SchemaVersion: 1,
		TenantID:      status.Key.Tenant,
		ProductID:     status.Key.Product,
		Revision:      status.Revision,
		Status:        value,
		Visible:       value == "published",
		Reasons:       reasons,
		At:            at,
	}
	v, d := status.Verdict, status.Decision
	if v == nil || d == nil || v.State().DecidedBy != moderation.Operator ||
		v.State().Status == moderation.Pending || v.State().Revision != status.Revision ||
		v.State().Decision == nil || !verdictDecisionMatches(v, status.Revision, *d) {
		return event
	}
	event.Decision = &OfferDecision{
		Action: d.Action,
		Reason: moderation.DecisionReason(d.Reason, d.ReasonMessage),
		Note:   d.Note,
		At:     d.At.UTC().Format(time.RFC3339),
	}
	if status.Publication.Published && d.Action == moderation.Reject {
		event.Reasons = rejectionReasons(event.Reasons, status.CaseReasons)
	}
	return event
}
