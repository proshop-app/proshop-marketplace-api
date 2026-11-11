// Package moderationstatus доставляет текущее состояние магазину через outbox.
package moderationstatus

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/domain/moderation"
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

// Имена задач стабильны: уже записанные строки outbox переживают обновление сервиса.
const (
	TaskOffer       = "moderation.offer.status"
	TaskParticipant = "moderation.participant.status"
)

// Reason различает структурный запрет и решение модерации для админки магазина.
type Reason struct {
	Source  string `json:"source"`
	Key     string `json:"key"`
	Message string `json:"message"`
}

// OfferDecision передаёт только сведения, предназначенные магазину.
type OfferDecision struct {
	Action moderation.Action      `json:"action"`
	Reason *moderation.ReasonCode `json:"reason"`
	Note   string                 `json:"note"`
	At     string                 `json:"at"`
}

// OfferEvent содержит ревизию проекции, чтобы магазин мог отбрасывать старые сообщения.
type OfferEvent struct {
	SchemaVersion int            `json:"schemaVersion"`
	TenantID      ids.TenantID   `json:"tenantId"`
	ProductID     ids.ProductID  `json:"productId"`
	Revision      int64          `json:"revision"`
	Status        string         `json:"status"`
	Visible       bool           `json:"visible"`
	Reasons       []Reason       `json:"reasons"`
	Decision      *OfferDecision `json:"decision"`
	At            string         `json:"at"`
}

// ParticipantEvent отделяет доступность магазина от допуска отдельных карточек.
type ParticipantEvent struct {
	Stage         participant.Stage      `json:"stage"`
	SchemaVersion int                    `json:"schemaVersion"`
	TenantID      ids.TenantID           `json:"tenantId"`
	Enabled       bool                   `json:"enabled"`
	Trust         string                 `json:"trust"`
	Reason        *moderation.ReasonCode `json:"reason"`
	By            string                 `json:"by"`
	Note          string                 `json:"note"`
	At            string                 `json:"at"`
}

type payload struct {
	TenantID  ids.TenantID  `json:"tenantId"`
	ProductID ids.ProductID `json:"productId,omitempty"`
}

// Service читает состояние в транзакции, а брокер вызывает после её завершения.
type Service struct {
	uow              ports.UnitOfWork
	clock            ports.Clock
	publisher        ports.Publisher
	offerTopic       string
	participantTopic string
}

// NewService принимает полные имена топиков, не связывая приложение с конфигурацией окружения.
func NewService(
	uow ports.UnitOfWork,
	clock ports.Clock,
	publisher ports.Publisher,
	offerTopic string,
	participantTopic string,
) *Service {
	return &Service{
		uow:              uow,
		clock:            clock,
		publisher:        publisher,
		offerTopic:       offerTopic,
		participantTopic: participantTopic,
	}
}

// OfferTask склеивает ожидающие изменения карточки и вердикта одного предложения.
func OfferTask(tenant ids.TenantID, product ids.ProductID, now time.Time) ports.NewTask {
	body, _ := json.Marshal(payload{TenantID: tenant, ProductID: product})
	return ports.NewTask{
		Kind:     TaskOffer,
		TenantID: tenant,
		DedupKey: TaskOffer + ":" + (ids.OfferID{Tenant: tenant, Product: product}).String(),
		Payload:  body,
		At:       now,
	}
}

// ParticipantTask склеивает решения участника: обработчик прочитает последнее.
func ParticipantTask(tenant ids.TenantID, now time.Time) ports.NewTask {
	body, _ := json.Marshal(payload{TenantID: tenant})
	return ports.NewTask{
		Kind:     TaskParticipant,
		TenantID: tenant,
		DedupKey: TaskParticipant + ":" + string(tenant),
		Payload:  body,
		At:       now,
	}
}

// Status не учитывает доступность магазина: она доставляется отдельным событием.
func Status(publication taxonomy.Publication, verdict *moderation.Verdict) (string, []Reason) {
	status := "hidden"
	reasons := make([]Reason, 0, len(publication.Issues)+1)
	for _, issue := range publication.Issues {
		message := "Карточка не прошла проверку структуры"
		if issue.Attribute != "" {
			message += ": " + issue.Attribute
		}
		reasons = append(reasons, Reason{Source: "taxonomy", Key: issue.Code, Message: message})
	}
	verdictReasons := []moderation.Reason{moderation.NotEvaluated()}
	if verdict != nil {
		state := verdict.State()
		verdictReasons = state.Reasons
		if state.Status == moderation.Pending {
			status = "pending"
		} else if publication.Published && state.Status == moderation.Allowed {
			return "published", []Reason{}
		}
	}
	for _, reason := range verdictReasons {
		reasons = append(reasons, Reason{Source: "moderation", Key: reason.Key, Message: reason.Message})
	}
	return status, reasons
}

// HandleTask читает последнее состояние, поэтому склейка не теряет поздние решения.
func (s *Service) HandleTask(ctx context.Context, task ports.Task) error {
	var p payload
	if err := json.Unmarshal(task.Payload, &p); err != nil {
		return moderation.ErrInvalid.Wrap(err)
	}
	if p.TenantID != task.TenantID {
		return moderation.ErrInvalid
	}
	if _, err := ids.ParseTenantID(string(p.TenantID)); err != nil {
		return moderation.ErrInvalid.Wrap(err)
	}
	message := ports.Message{Headers: map[string]string{"x-tenant": string(p.TenantID)}}
	err := s.uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		var body any
		at := s.clock.Now().UTC().Format(time.RFC3339)
		switch task.Kind {
		case TaskOffer:
			if _, err := ids.ParseProductID(string(p.ProductID)); err != nil {
				return moderation.ErrInvalid.Wrap(err)
			}
			state, err := tx.Offers().Offer(ctx, p.TenantID, p.ProductID)
			if errors.Is(err, catalog.ErrProductNotFound) {
				return nil
			}
			if err != nil {
				return err
			}
			product := state.Product.State()
			if product.Deleted {
				return nil
			}
			status, reasons := Status(product.Card.Publication, state.Verdict)
			decision, rejectedReasons, err := currentDecision(ctx, tx, p, state.Verdict, product.Revision)
			if err != nil {
				return err
			}
			if decision != nil && decision.Action == moderation.Reject {
				reasons = rejectionReasons(reasons, rejectedReasons)
			}
			body = OfferEvent{
				Decision:      decision,
				SchemaVersion: 1,
				TenantID:      p.TenantID,
				ProductID:     p.ProductID,
				Revision:      product.Revision,
				Status:        status,
				Visible:       status == "published",
				Reasons:       reasons,
				At:            at,
			}
			message.Topic = s.offerTopic
			message.Key, _ = json.Marshal([]string{string(p.TenantID), string(p.ProductID)})
		case TaskParticipant:
			member, err := tx.Participants().Get(ctx, p.TenantID)
			if err != nil {
				return err
			}
			history, _, err := tx.ParticipantDecisions().List(ctx, p.TenantID, participant.Page{Limit: 1})
			if err != nil {
				return err
			}
			state := member.State()
			event := ParticipantEvent{
				By:            "operator",
				SchemaVersion: 1,
				TenantID:      p.TenantID,
				Enabled:       state.Enabled,
				Stage:         state.Stage,
				Trust:         state.Trust,
				At:            at,
			}
			if len(history) > 0 {
				d := history[0]
				event.Reason = moderation.DecisionReason(d.Reason, d.ReasonMessage)
				event.Note, event.By = d.Note, d.Actor
			}
			body = event
			message.Topic, message.Key = s.participantTopic, []byte(p.TenantID)
		default:
			return moderation.ErrInvalid
		}
		var err error
		message.Value, err = json.Marshal(body)
		return err
	})
	if err != nil || message.Value == nil {
		return err
	}
	return s.publisher.Publish(ctx, message)
}

func currentDecision(
	ctx context.Context,
	tx ports.Tx,
	p payload,
	verdict *moderation.Verdict,
	revision int64,
) (*OfferDecision, []moderation.Reason, error) {
	if verdict == nil {
		return nil, nil, nil
	}
	v := verdict.State()
	if v.DecidedBy != moderation.Operator || v.Status == moderation.Pending ||
		v.Revision != revision || v.Decision == nil {
		return nil, nil, nil
	}
	history, err := tx.ModerationDecisions().List(ctx, p.TenantID, p.ProductID)
	if err != nil {
		return nil, nil, err
	}
	if len(history) > 0 {
		d := history[0]
		if d.Revision <= revision && d.Action == v.Decision.Action && d.Operator == v.Operator &&
			d.At.Equal(v.Decision.At) && d.Note == v.Decision.Note &&
			reflect.DeepEqual(moderation.DecisionReason(d.Reason, d.ReasonMessage), v.Decision.Reason) {
			var reasons []moderation.Reason
			if d.Action == moderation.Reject {
				c, err := tx.ModerationCases().Get(ctx, d.CaseID)
				if err != nil {
					return nil, nil, err
				}
				reasons = c.State().Reasons
			}
			return &OfferDecision{
				Action: d.Action,
				Reason: moderation.DecisionReason(d.Reason, d.ReasonMessage),
				Note:   d.Note,
				At:     d.At.UTC().Format(time.RFC3339),
			}, reasons, nil
		}
	}
	return nil, nil, nil
}

func rejectionReasons(current []Reason, reasons []moderation.Reason) []Reason {
	result := make([]Reason, 0, len(current)+len(reasons))
	for _, reason := range current {
		if reason.Source == "taxonomy" {
			result = append(result, reason)
		}
	}
	for _, reason := range reasons {
		result = append(result, Reason{Source: "moderation", Key: reason.Key, Message: reason.Message})
	}
	return result
}
