// Package synonyms управляет допуском, публикацией и снятием поисковых направлений.
package synonyms

import (
	"context"
	"encoding/json"
	"strconv"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/synonyms"
)

// TaskChanged связывает атомарную публикацию снимка с доставкой сигнала через outbox.
const (
	TaskChanged = "search-synonyms.changed"
	EventTopic  = "catalog.search-synonyms.changed"
	eventKey    = "search_synonyms"
	MaxPage     = 100
)

// Service меняет один словарь площадки и публикует его целыми снимками.
type Service struct {
	uow       ports.UnitOfWork
	clock     ports.Clock
	validator ports.SynonymValidator
	model     ports.SynonymModel
	publisher ports.Publisher
	topic     string
}

// Event сообщает потребителю номер ревизии для фоновой сверки.
type Event struct {
	SchemaVersion int   `json:"schemaVersion"`
	Revision      int64 `json:"revision"`
}

// Page перечисляет направления по устойчивому курсору, даже если меняются их статусы.
type Page struct {
	Entries   []synonyms.Entry `json:"entries"`
	NextAfter string           `json:"nextAfter,omitempty"`
	Revision  int64            `json:"revision"`
}

// StopList даёт оператору значения и ревизию для условной записи.
type StopList struct {
	Revision     int64    `json:"revision"`
	BlockedTerms []string `json:"blockedTerms"`
}

// OperatorChange позволяет сверить голову и записать автора операции в журнал.
type OperatorChange struct {
	ExpectedRevision int64
	Actor            string
	Reason           string
}

type session struct {
	tx   ports.Tx
	repo ports.SynonymRepository
	head ports.SynonymHead
	now  time.Time
}

// New собирает словарь с необязательным валидатором для установок без токена читателя.
func New(
	uow ports.UnitOfWork,
	clock ports.Clock,
	validator ports.SynonymValidator,
	publisher ports.Publisher,
	topic string,
) *Service {
	return &Service{
		uow:       uow,
		clock:     clock,
		validator: validator,
		publisher: publisher,
		topic:     topic,
	}
}

// WithModel подключает источник предложений, ограниченный карантином.
func (s *Service) WithModel(model ports.SynonymModel) *Service {
	s.model = model
	return s
}

func (s *Service) transact(ctx context.Context, lock bool, run func(context.Context, *session) error) error {
	return s.uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		repo := tx.Synonyms()
		head, err := repo.Current(ctx, lock)
		if err != nil {
			return err
		}
		return run(ctx, &session{
			tx:   tx,
			repo: repo,
			head: head,
			now:  s.clock.Now().UTC(),
		})
	})
}

// Snapshot передаёт хранилищу известную ревизию, чтобы не читать тело при совпадении.
func (s *Service) Snapshot(ctx context.Context, known int64) (int64, []byte, error) {
	var revision int64
	var body []byte
	err := s.uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		var err error
		revision, body, err = tx.Synonyms().Snapshot(ctx, known)
		return err
	})
	return revision, body, err
}

// List возвращает ограниченную страницу с источниками и проверками.
func (s *Service) List(ctx context.Context, after string, limit int) (Page, error) {
	if limit < 1 || limit > MaxPage || len(after) > 64 {
		return Page{}, synonyms.ErrInvalid
	}
	var page Page
	err := s.transact(ctx, false, func(ctx context.Context, m *session) error {
		items, err := m.repo.Entries(ctx, m.now, after, limit+1)
		if err != nil {
			return err
		}
		page = Page{Entries: items, Revision: m.head.Revision}
		if len(items) > limit {
			page.Entries = items[:limit]
			page.NextAfter = items[limit-1].ID
		}
		return nil
	})
	return page, err
}

// StopList читает термины и соответствующую ревизию одним чтением.
func (s *Service) StopList(ctx context.Context) (StopList, error) {
	var result StopList
	err := s.transact(ctx, false, func(_ context.Context, m *session) error {
		result = m.stopList()
		return nil
	})
	return result, err
}

func (m *session) stopList() StopList {
	return StopList{Revision: m.head.Revision, BlockedTerms: m.head.BlockedTerms}
}

func (m *session) transition(ctx context.Context, before, after synonyms.Entry, move synonyms.Transition) error {
	if err := m.repo.Save(ctx, before, after); err != nil {
		return err
	}
	return m.repo.RecordTransition(ctx, after.ID, move)
}

func (m *session) disable(ctx context.Context, entry synonyms.Entry, actor, reason string) error {
	before := entry
	move, err := entry.Disable(m.now, actor, reason)
	if err != nil {
		return err
	}
	return m.transition(ctx, before, entry, move)
}

func (m *session) publish(ctx context.Context, rules []synonyms.Rule) error {
	body, err := m.snapshot(rules)
	if err != nil {
		return err
	}
	if len(body) > synonyms.MaxSnapshot {
		return synonyms.ErrConflict.WithMessage("превышен размер снимка поискового словаря")
	}
	m.head.Revision++
	if err := m.repo.SaveHead(ctx, m.head, body); err != nil {
		return err
	}
	event, err := json.Marshal(Event{SchemaVersion: synonyms.SchemaVersion, Revision: m.head.Revision})
	if err != nil {
		return err
	}
	return m.tx.Outbox().Enqueue(ctx, ports.NewTask{
		Kind:     TaskChanged,
		DedupKey: TaskChanged + ":" + strconv.FormatInt(m.head.Revision, 10),
		Payload:  event,
		At:       m.now,
	})
}

func (m *session) snapshot(rules []synonyms.Rule) ([]byte, error) {
	return json.Marshal(synonyms.Snapshot{
		SchemaVersion: synonyms.SchemaVersion,
		Revision:      m.head.Revision + 1,
		Rules:         rules,
		BlockedTerms:  m.head.BlockedTerms,
	})
}

func ruleList(entries []synonyms.Entry) []synonyms.Rule {
	rules := make([]synonyms.Rule, 0, len(entries))
	for _, entry := range entries {
		rules = append(rules, entry.Rule)
	}
	return rules
}

// HandleTask отправляет сигнал целого снимка с постоянным ключом партиции.
func (s *Service) HandleTask(ctx context.Context, task ports.Task) error {
	return s.publisher.Publish(ctx, ports.Message{
		Topic: s.topic,
		Key:   []byte(eventKey),
		Value: task.Payload,
	})
}
