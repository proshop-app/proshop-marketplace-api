package messaging

import (
	"context"
	"encoding/json"
	"strconv"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/event"
	"github.com/proshop-app/proshop-marketplace/internal/domain/operator"
	"github.com/proshop-app/proshop-marketplace/pkg/errs"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

// TaskReplay — повтор исходного сообщения через существующий outbox.
const TaskReplay = "messaging.event.replay"

// Events — операторские решения и надёжное выполнение повторов.
type Events struct {
	uow    ports.UnitOfWork
	clock  ports.Clock
	replay ports.EventReplay
}

// EventPage — страница запаркованных сообщений.
type EventPage struct {
	Items  []ports.ParkedEvent
	Total  int
	Limit  int
	Offset int
}

// NewEvents собирает сценарии без зависимости от брокера и HTTP.
func NewEvents(uow ports.UnitOfWork, clock ports.Clock, replay ports.EventReplay) *Events {
	return &Events{uow: uow, clock: clock, replay: replay}
}

// List проверяет роль и фильтры до обращения к хранилищу.
func (s *Events) List(ctx context.Context, actor operator.Principal, f ports.EventFilter) (EventPage, error) {
	if err := operator.Require(actor); err != nil {
		return EventPage{}, err
	}
	if f.Limit == 0 {
		f.Limit = 50
	}
	if f.Limit < 1 || f.Limit > 200 || f.Offset < 0 ||
		(f.Status != "" && f.Status != event.Parked && f.Status != event.Queued && f.Status != event.Resolved) {
		return EventPage{}, event.ErrFilter
	}
	if f.TenantID != "" {
		if _, err := ids.ParseTenantID(f.TenantID); err != nil {
			return EventPage{}, err
		}
	}
	page := EventPage{Limit: f.Limit, Offset: f.Offset}
	err := s.uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		var err error
		page.Items, page.Total, err = tx.Parked().List(ctx, f)
		return err
	})
	return page, err
}

// Decide атомарно сохраняет решение, аудит и задачу повторной обработки.
func (s *Events) Decide(
	ctx context.Context,
	actor operator.Principal,
	id int64,
	retry bool,
	note string,
) (ports.ParkedEvent, error) {
	if err := operator.Require(actor); err != nil {
		return ports.ParkedEvent{}, err
	}
	var result ports.ParkedEvent
	err := s.uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		stored, err := tx.Parked().Get(ctx, id)
		if err != nil {
			return err
		}
		aggregate := event.Restore(stored.Control)
		now := s.clock.Now()
		if err := aggregate.Decide(retry, note, actor.ID, now); err != nil {
			return err
		}
		if err := tx.Parked().Update(ctx, aggregate, "", 0); err != nil {
			return err
		}
		if retry {
			payload, err := json.Marshal(id)
			if err != nil {
				return err
			}
			if err := tx.Outbox().Enqueue(ctx, ports.NewTask{
				Kind:     TaskReplay,
				TenantID: ids.TenantID(stored.TenantID),
				DedupKey: TaskReplay + ":" + strconv.FormatInt(id, 10),
				Payload:  payload,
				At:       now,
			}); err != nil {
				return err
			}
		}
		stored.Control = aggregate.State()
		result = stored
		return nil
	})
	return result, err
}

// HandleTask сохраняет идентичность Kafka для дедупа при потере ответа/перезапуске.
func (s *Events) HandleTask(ctx context.Context, task ports.Task) error {
	var id int64
	if err := json.Unmarshal(task.Payload, &id); err != nil {
		return err
	}
	var stored ports.ParkedEvent
	err := s.uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		var err error
		stored, err = tx.Parked().Get(ctx, id)
		return err
	})
	if err != nil {
		return err
	}
	if stored.Control.Status != event.Queued {
		return nil
	}
	replayErr := s.replay.Replay(ctx, stored)
	if replayErr != nil && errs.CodeOf(replayErr) != errs.Invalid && task.Attempts < 5 {
		return replayErr
	}
	return s.uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		aggregate := event.Restore(stored.Control)
		if err := aggregate.Finish(replayErr == nil, s.clock.Now()); err != nil {
			return err
		}
		failure := ""
		if replayErr != nil {
			failure = replayErr.Error()
		}
		return tx.Parked().Update(ctx, aggregate, failure, max(1, task.Attempts))
	})
}
