package moderation

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/moderation"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

// TaskReevaluate отделяет переоценку политики от синхронизации индекса.
const TaskReevaluate = "moderation.tenant.reevaluate"

// TaskAllowTrusted продолжает фоновое одобрение по одной странице на аренду outbox.
const TaskAllowTrusted = "moderation.tenant.allow_trusted"

// TaskReevaluateAll раздаёт страницы участников после фиксации правки правил.
const TaskReevaluateAll = "moderation.all.reevaluate"

const batchSize = 200

type taskPayload struct {
	TenantID     ids.TenantID  `json:"tenantId"`
	After        ids.UUID      `json:"after,omitempty"`
	ProductAfter ids.ProductID `json:"productAfter,omitempty"`
	Traversal    int64         `json:"traversal,omitempty"`
}

type reevaluateAllPayload struct {
	After ids.TenantID `json:"after,omitempty"`
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

// AllowTrustedTask не запускает переоценку дел с другими причинами.
func AllowTrustedTask(tenant ids.TenantID, after ids.UUID, now time.Time) ports.NewTask {
	payload, _ := json.Marshal(taskPayload{TenantID: tenant, After: after})
	return ports.NewTask{
		Kind:     TaskAllowTrusted,
		TenantID: tenant,
		DedupKey: TaskAllowTrusted + ":" + string(tenant) + ":" + string(after),
		Payload:  payload,
		At:       now,
	}
}

// EnqueueAll ставит один обход, который будет читать уже зафиксированные правила.
func (s *Service) EnqueueAll(ctx context.Context) error {
	return s.uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		return tx.Outbox().Enqueue(ctx, reevaluateAllTask(reevaluateAllPayload{}, s.clock.Now()))
	})
}

func reevaluateAllTask(payload reevaluateAllPayload, now time.Time) ports.NewTask {
	raw, _ := json.Marshal(payload)
	key := TaskReevaluateAll
	if payload.After != "" {
		key += ":" + string(payload.After)
	}
	return ports.NewTask{
		Kind:     TaskReevaluateAll,
		DedupKey: key,
		Payload:  raw,
		At:       now,
	}
}

// HandleReevaluateAll раздаёт одну страницу без чтения и блокировки правил.
// Новая правка после взятия начальной задачи ставит новый обход с первого магазина.
func (s *Service) HandleReevaluateAll(ctx context.Context, task ports.Task) error {
	var payload reevaluateAllPayload
	if err := json.Unmarshal(task.Payload, &payload); err != nil {
		return moderation.ErrInvalid.Wrap(err)
	}
	if task.Kind != TaskReevaluateAll || task.TenantID != "" {
		return moderation.ErrInvalid
	}
	if payload.After != "" {
		if _, err := ids.ParseTenantID(string(payload.After)); err != nil {
			return moderation.ErrInvalid
		}
	}
	return s.uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		tenants, err := tx.Participants().TenantIDsAfter(ctx, payload.After, batchSize+1)
		if err != nil {
			return err
		}
		now := s.clock.Now()
		tasks := make([]ports.NewTask, 0, len(tenants))
		for _, tenant := range tenants[:min(len(tenants), batchSize)] {
			tasks = append(tasks, ReevaluateTask(tenant, now))
		}
		if len(tenants) > batchSize {
			payload.After = tenants[batchSize-1]
			tasks = append(tasks, reevaluateAllTask(payload, now))
		}
		return tx.Outbox().EnqueueBatch(ctx, tasks)
	})
}

// HandleTask проверяет совпадение участника в задаче и её полезной нагрузке.
func (s *Service) HandleTask(ctx context.Context, task ports.Task) error {
	var payload taskPayload
	if err := json.Unmarshal(task.Payload, &payload); err != nil {
		return moderation.ErrInvalid.Wrap(err)
	}
	if (task.Kind != TaskReevaluate && task.Kind != TaskAllowTrusted) || payload.TenantID != task.TenantID {
		return moderation.ErrInvalid
	}
	if _, err := ids.ParseTenantID(string(payload.TenantID)); err != nil {
		return moderation.ErrInvalid.Wrap(err)
	}
	if task.Kind == TaskAllowTrusted {
		return s.allowTrustedProbation(ctx, payload.TenantID, payload.After, task.ID)
	}
	return s.reevaluateTaskPage(ctx, task, payload)
}

// Длинная переоценка продолжает курсор отдельной задачей, освобождая слот
// общего диспетчера. Оценка страницы и её продолжение фиксируются вместе.
func (s *Service) reevaluateTaskPage(ctx context.Context, task ports.Task, payload taskPayload) error {
	if payload.ProductAfter != "" {
		if _, err := ids.ParseProductID(string(payload.ProductAfter)); err != nil {
			return moderation.ErrInvalid.Wrap(err)
		}
	}
	return s.uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		states, err := tx.Offers().TenantOffers(ctx, payload.TenantID, payload.ProductAfter, batchSize)
		if err != nil {
			return err
		}
		now := s.clock.Now()
		if err := s.evaluatePage(ctx, tx, states, now); err != nil {
			return err
		}
		if len(states) < batchSize {
			return nil
		}
		payload.ProductAfter = states[len(states)-1].Product.State().ProductID
		if payload.Traversal == 0 {
			payload.Traversal = task.ID
		}
		raw, _ := json.Marshal(payload)
		key := fmt.Sprintf("%s:%s:%d:%s", TaskReevaluate, payload.TenantID, payload.Traversal, payload.ProductAfter)
		return tx.Outbox().Enqueue(ctx, ports.NewTask{
			Kind:     TaskReevaluate,
			TenantID: payload.TenantID,
			DedupKey: key,
			Payload:  raw,
			At:       now,
		})
	})
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
			if count > 0 {
				after = states[count-1].Product.State().ProductID
			}
			return s.evaluatePage(ctx, tx, states, s.clock.Now())
		})
		if err != nil {
			return err
		}
		if count < batchSize {
			return nil
		}
	}
}

func (s *Service) evaluatePage(
	ctx context.Context,
	tx ports.Tx,
	states []ports.OfferState,
	now time.Time,
) error {
	var tasks []ports.NewTask
	for _, state := range states {
		decision, err := s.evaluate(ctx, tx, state.Product, state.Participant, nil, now)
		if err != nil {
			return err
		}
		if decision.Changed {
			product := state.Product.State()
			tasks = append(tasks, evaluationTasks(product, now)...)
		}
	}
	return tx.Outbox().EnqueueBatch(ctx, tasks)
}
