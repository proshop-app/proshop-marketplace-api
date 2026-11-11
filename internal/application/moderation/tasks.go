package moderation

import (
	"context"
	"encoding/json"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/moderation"
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

// TaskReevaluate отделяет переоценку политики от синхронизации индекса.
const TaskReevaluate = "moderation.tenant.reevaluate"
const batchSize = 200

type taskPayload struct {
	TenantID ids.TenantID `json:"tenantId"`
}

// ReevaluateTask склеивает ожидающие задачи одного участника.
func ReevaluateTask(tenant ids.TenantID, now time.Time) ports.NewTask {
	payload, _ := json.Marshal(taskPayload{TenantID: tenant})
	return ports.NewTask{
		Kind:     TaskReevaluate,
		TenantID: tenant,
		DedupKey: TaskReevaluate + ":" + string(tenant),
		Payload:  payload,
		At:       now,
	}
}

// EnqueueAll обходит участников страницами без загрузки всего реестра.
func EnqueueAll(ctx context.Context, tx ports.Tx, now time.Time) error {
	for offset := 0; ; offset += batchSize {
		members, _, err := tx.Participants().List(ctx, participant.Page{Limit: batchSize, Offset: offset})
		if err != nil {
			return err
		}
		for _, member := range members {
			if err := tx.Outbox().Enqueue(ctx, ReevaluateTask(member.TenantID(), now)); err != nil {
				return err
			}
		}
		if len(members) < batchSize {
			return nil
		}
	}
}

// EnqueueAll обходит участников страницами без загрузки всего реестра.
func (s *Service) EnqueueAll(ctx context.Context) error {
	return s.uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error { return EnqueueAll(ctx, tx, s.clock.Now()) })
}

// HandleTask проверяет совпадение участника в задаче и её полезной нагрузке.
func (s *Service) HandleTask(ctx context.Context, task ports.Task) error {
	var payload taskPayload
	if err := json.Unmarshal(task.Payload, &payload); err != nil {
		return moderation.ErrInvalid.Wrap(err)
	}
	if task.Kind != TaskReevaluate || payload.TenantID != task.TenantID {
		return moderation.ErrInvalid
	}
	if _, err := ids.ParseTenantID(string(payload.TenantID)); err != nil {
		return moderation.ErrInvalid.Wrap(err)
	}
	return s.Reevaluate(ctx, payload.TenantID)
}

// Reevaluate пересчитывает предложения страницами с условным сохранением.
func (s *Service) Reevaluate(ctx context.Context, tenant ids.TenantID) error {
	var after ids.ProductID
	for {
		count := 0
		err := s.uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
			states, err := tx.Offers().TenantOffers(ctx, tenant, after, batchSize)
			if err != nil {
				return err
			}
			count = len(states)
			for _, state := range states {
				if err := s.Evaluate(ctx, tx, state.Product, state.Participant, nil, s.clock.Now()); err != nil {
					return err
				}
				after = state.Product.State().ProductID
			}
			return nil
		})
		if err != nil {
			return err
		}
		if count < batchSize {
			return nil
		}
	}
}
