package messaging

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/event"
	"github.com/proshop-app/proshop-marketplace/internal/domain/operator"
	"github.com/proshop-app/proshop-marketplace/pkg/errs"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

// TaskReplay — повтор исходного сообщения через существующий outbox.
const TaskReplay = "messaging.event.replay"

// DecisionTTL — срок повторного получения первоначального успешного ответа.
const DecisionTTL = 24 * time.Hour

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

// replayTask связывает бюджет попыток outbox с единственным решением оператора.
type replayTask struct {
	EventID         int64 `json:"eventId"`
	DecisionVersion int64 `json:"decisionVersion"`
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
	expectedVersion int64,
	retry bool,
	note string,
) (ports.ParkedEvent, error) {
	return s.DecideWithKey(ctx, actor, id, expectedVersion, retry, note, "")
}

// DecideWithKey сохраняет успешный ответ атомарно; пустой ключ оставляет проверку версии.
func (s *Events) DecideWithKey(
	ctx context.Context,
	actor operator.Principal,
	id int64,
	expectedVersion int64,
	retry bool,
	note string,
	key string,
) (ports.ParkedEvent, error) {
	if err := operator.Require(actor); err != nil {
		return ports.ParkedEvent{}, err
	}
	identity, _ := json.Marshal([]string{actor.ID, key})
	storageKey := fmt.Sprintf("%x", sha256.Sum256(identity))
	request, _ := json.Marshal([]any{id, expectedVersion, retry, note})
	fingerprint := fmt.Sprintf("%x", sha256.Sum256(request))
	var result ports.ParkedEvent
	err := s.uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		if key != "" {
			cached, found, err := tx.Parked().Decision(ctx, storageKey, s.clock.Now())
			if err != nil {
				return err
			}
			if found {
				if cached.Fingerprint != fingerprint {
					return event.ErrKeyReused
				}
				result = cached.Result
				return nil
			}
		}
		stored, err := tx.Parked().Get(ctx, id)
		if err != nil {
			return err
		}
		result = stored
		if expectedVersion != stored.Control.Version {
			return event.ErrConflict
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
			decision := replayTask{EventID: id, DecisionVersion: aggregate.State().Version}
			payload, err := json.Marshal(decision)
			if err != nil {
				return err
			}
			if err := tx.Outbox().Enqueue(ctx, ports.NewTask{
				Kind:     TaskReplay,
				TenantID: ids.TenantID(stored.TenantID),
				DedupKey: TaskReplay + ":" + strconv.FormatInt(id, 10) + ":" +
					strconv.FormatInt(decision.DecisionVersion, 10),
				Payload: payload,
				At:      now,
			}); err != nil {
				return err
			}
		}
		stored.Control = aggregate.State()
		result = stored
		if key != "" {
			return tx.Parked().SaveDecision(ctx, storageKey, ports.EventDecision{
				Fingerprint: fingerprint,
				Result:      result,
				ExpiresAt:   now.Add(DecisionTTL),
			})
		}
		return nil
	})
	if errors.Is(err, event.ErrConflict) {
		// CAS мог проиграть уже после чтения: возвращаем актуальный снимок вне откатившейся транзакции.
		readErr := s.uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
			var err error
			result, err = tx.Parked().Get(ctx, id)
			return err
		})
		if readErr != nil {
			return ports.ParkedEvent{}, readErr
		}
	}
	return result, err
}

// HandleTask сохраняет идентичность Kafka для дедупа при потере ответа/перезапуске.
func (s *Events) HandleTask(ctx context.Context, task ports.Task) error {
	var decision replayTask
	if err := json.Unmarshal(task.Payload, &decision); err != nil {
		return err
	}
	var stored ports.ParkedEvent
	err := s.uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		var err error
		stored, err = tx.Parked().Get(ctx, decision.EventID)
		return err
	})
	if err != nil {
		return err
	}
	if stored.Control.Version != decision.DecisionVersion || stored.Control.Status != event.Queued {
		slog.InfoContext(
			ctx,
			"пропущена устаревшая задача повтора",
			"eventId",
			decision.EventID,
			"decisionVersion",
			decision.DecisionVersion,
			"currentVersion",
			stored.Control.Version,
			"tenantId",
			stored.TenantID,
		)
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
