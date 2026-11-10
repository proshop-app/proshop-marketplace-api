package reference

import (
	"context"
	"encoding/json"
	"strconv"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/reference"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

// TaskChanged — задача outbox: опубликовать catalog.reference.changed.
const TaskChanged = "reference.changed"

// Event — тело catalog.reference.changed. Поля только добавляются: Truncated
// сообщает, что списки обрезаны и потребителю нужна дельта снимка.
type Event struct {
	SchemaVersion int                   `json:"schemaVersion"`
	Dictionary    string                `json:"dictionary"`
	RevisionFrom  int64                 `json:"revisionFrom"`
	RevisionTo    int64                 `json:"revisionTo"`
	EntryIDs      []ids.UUID            `json:"entryIds"`
	Kinds         []reference.EventKind `json:"kinds"`
	AffectedKeys  []string              `json:"affectedKeys"`
	Truncated     bool                  `json:"truncated"`
}

// session — одна мутация словаря под блокировкой его строки.
type session struct {
	repo      reference.Repository
	tx        ports.Tx
	dict      *reference.Dictionary
	from      int64
	now       time.Time
	dirty     bool
	truncated bool
	effects   reference.Effects
}

// mutate открывает транзакцию мутации словаря: блокировка строки словаря,
// сценарий, голова журнала и задача события фиксируются вместе. Возвращает
// голову словаря после сценария из той же транзакции.
func (s *Service) mutate(
	ctx context.Context,
	code string,
	fn func(ctx context.Context, m *session) error,
) (int64, error) {
	var revision int64
	err := s.uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		var err error
		revision, err = s.mutateInTransaction(ctx, tx, code, fn)
		return err
	})
	if err != nil {
		return 0, err
	}
	return revision, nil
}

// mutateInTransaction сохраняет тот же журнал и outbox внутри общей транзакции пакета.
func (s *Service) mutateInTransaction(
	ctx context.Context,
	tx ports.Tx,
	code string,
	fn func(ctx context.Context, m *session) error,
) (int64, error) {
	repo := tx.Reference()
	dict, err := repo.LockDictionary(ctx, code)
	if err != nil {
		return 0, err
	}
	if err := dict.CheckNormalizer(); err != nil {
		return 0, err
	}
	m := &session{repo: repo, tx: tx, dict: dict, from: dict.Revision(), now: s.clock.Now()}
	if err := fn(ctx, m); err != nil {
		return 0, err
	}
	if m.dirty {
		if err := repo.SaveDictionary(ctx, dict); err != nil {
			return 0, err
		}
		if err := m.enqueue(ctx); err != nil {
			return 0, err
		}
	}
	return dict.Revision(), nil
}

// record пишет изменение: запись журнала первой, затем занятость ключей,
// лимиты и сами записи. Без изменения содержимого ревизии нет.
func (m *session) record(
	ctx context.Context,
	draft *reference.Draft,
	kind reference.ChangeKind,
	primary ids.UUID,
	meta reference.Meta,
	reverts int64,
) (bool, error) {
	change, ok := draft.Change(kind, primary, meta, m.now)
	if !ok {
		return false, nil
	}
	change.Reverts = reverts
	revision, err := m.repo.Append(ctx, change)
	if err != nil {
		return false, err
	}
	change.Revision = revision
	if err := m.checkKeys(ctx, draft); err != nil {
		return false, err
	}
	if err := m.dict.Advance(revision, draft.EntriesDelta()); err != nil {
		return false, err
	}
	saved := draft.Saved()
	for _, e := range saved {
		e.Touch(revision, m.now)
	}
	if err := m.repo.Save(ctx, saved, draft.Deleted()); err != nil {
		return false, err
	}
	m.dirty = true
	m.effects = m.effects.Join(change.Effects())
	// Для события достаточно ограниченных списков и признака: полная дельта остаётся в журнале.
	if len(m.effects.Entries) > maxEventList {
		m.effects.Entries = m.effects.Entries[:maxEventList]
		m.truncated = true
	}
	if len(m.effects.Keys) > maxEventList {
		m.effects.Keys = m.effects.Keys[:maxEventList]
		m.truncated = true
	}
	return true, nil
}

// checkKeys: ключ варианта уникален в словаре, второй претендент — конфликт.
// Ключ, который в этом же изменении отдаёт другая запись, свободен.
func (m *session) checkKeys(ctx context.Context, draft *reference.Draft) error {
	claimed := draft.ClaimedKeys()
	if len(claimed) == 0 {
		return nil
	}
	keys := make([]string, 0, len(claimed))
	for key := range claimed {
		keys = append(keys, key)
	}
	owners, err := m.repo.KeyOwners(ctx, m.dict.Code(), keys)
	if err != nil {
		return err
	}
	for key, owner := range owners {
		if owner != claimed[key] && !draft.Releases(key, owner) {
			message := "написание «" + key + "» уже принадлежит записи " + string(owner)
			return reference.ErrKeyTaken.WithMessage(message)
		}
	}
	return nil
}

// enqueue ставит событие в outbox той же транзакцией. Изменение без следствий
// для ссылок (только свободные поля) события не порождает: снимок его отдаст.
func (m *session) enqueue(ctx context.Context) error {
	if len(m.effects.Kinds) == 0 {
		return nil
	}
	event := Event{
		SchemaVersion: SchemaVersion,
		Dictionary:    m.dict.Code(),
		RevisionFrom:  m.from,
		RevisionTo:    m.dict.Revision(),
		EntryIDs:      m.effects.Entries,
		Kinds:         m.effects.Kinds,
		AffectedKeys:  m.effects.Keys,
		Truncated:     m.truncated,
	}
	if len(event.EntryIDs) > maxEventList {
		event.EntryIDs, event.Truncated = event.EntryIDs[:maxEventList], true
	}
	if len(event.AffectedKeys) > maxEventList {
		event.AffectedKeys, event.Truncated = event.AffectedKeys[:maxEventList], true
	}
	payload, err := json.Marshal(event)
	if err != nil {
		return err
	}
	return m.tx.Outbox().Enqueue(ctx, ports.NewTask{
		Kind:     TaskChanged,
		DedupKey: TaskChanged + ":" + event.Dictionary + ":" + strconv.FormatInt(event.RevisionTo, 10),
		Payload:  payload,
		At:       m.now,
	})
}

// HandleTask публикует событие из outbox; ключ сообщения — код словаря,
// поэтому события одного словаря идут в одну партицию по порядку.
func (s *Service) HandleTask(ctx context.Context, task ports.Task) error {
	var event Event
	if err := json.Unmarshal(task.Payload, &event); err != nil {
		return reference.ErrInvalid.Wrap(err)
	}
	return s.publisher.Publish(ctx, ports.Message{
		Topic: s.config.Topic,
		Key:   []byte(event.Dictionary),
		Value: task.Payload,
	})
}
