// Package promotion содержит правила платного продвижения товаров.
package promotion

import (
	"strings"
	"time"

	"github.com/proshop-app/proshop-marketplace/pkg/ids"
	"github.com/proshop-app/proshop-marketplace/pkg/money"
)

// Kind перечисляет виды продвижения; M1 создаёт только товарный слот.
type Kind string

// Status фиксирует жизненный цикл без возобновления остановленного размещения.
type Status string

// Action отделяет решение от статуса оплаты.
type Action string

// Значения контракта продвижения не зависят от конкретного магазина.
const (
	SponsoredSlot Kind   = "sponsored_slot"
	Banner        Kind   = "banner"
	Showcase      Kind   = "showcase"
	Draft         Status = "draft"
	Review        Status = "review"
	Approved      Status = "approved"
	Rejected      Status = "rejected"
	Active        Status = "active"
	Finished      Status = "finished"
	Stopped       Status = "stopped"
	Submit        Action = "submit"
	Approve       Action = "approve"
	Reject        Action = "reject"
	Pay           Action = "pay"
	Finish        Action = "finish"
	Stop          Action = "stop"
	Pending              = "pending"
	Paid                 = "paid"
)

// Target сохраняет категорию площадки на момент заявки.
type Target struct {
	OfferID    ids.OfferID
	CategoryID string
	SaleID     string
	StoreID    string
}

// Marking хранит реквизиты рекламодателя и обязательный при одобрении erid.
type Marking struct {
	Advertiser string `json:"advertiser"`
	INN        string `json:"inn,omitempty"`
	ERID       string `json:"erid,omitempty"`
}

// Payment хранит оплату независимо от жизненного цикла размещения.
type Payment struct {
	Status   string
	PaidOn   string
	Amount   money.Money
	Document string
	Note     string
}

// CampaignState снимок размещения для сохранения и контроля конкурирующих изменений.
type CampaignState struct {
	ID              ids.UUID
	TenantID        ids.TenantID
	Kind            Kind
	PlacementCode   string
	Position        int
	Target          Target
	Period          Period
	Price           money.Money
	Marking         Marking
	Payment         Payment
	Status          Status
	Version         int64
	CreatedAt       time.Time
	UpdatedAt       time.Time
	Title           string
	RejectionReason string
}

// Campaign меняет размещение только через проверенные переходы.
type Campaign struct {
	state  CampaignState
	origin CampaignState
}

// Decision неизменяемая запись журнала решений.
type Decision struct {
	At     time.Time `json:"at"`
	Actor  string    `json:"actor"`
	Action Action    `json:"action"`
	Note   string    `json:"note,omitempty"`
}

// NewCampaign создаёт черновик с новой версией и неоплаченным состоянием.
func NewCampaign(s CampaignState, now time.Time, location *time.Location) (*Campaign, error) {
	if s.Kind != SponsoredSlot {
		return nil, ErrKindUnsupported
	}
	if _, err := ids.ParseUUID(string(s.ID)); err != nil {
		return nil, ErrInvalid
	}
	if err := validateInput(s, now, location); err != nil {
		return nil, err
	}
	s.Status = Draft
	s.Payment = Payment{Status: Pending}
	s.Version = 1
	s.CreatedAt = now
	s.UpdatedAt = now
	s.RejectionReason = ""
	return &Campaign{state: s}, nil
}

func validateInput(s CampaignState, now time.Time, location *time.Location) error {
	if s.Kind != SponsoredSlot {
		return ErrKindUnsupported
	}
	if s.PlacementCode != "category_slot" && s.PlacementCode != "search_slot" {
		return ErrInvalid
	}
	if s.TenantID == "" || s.Target.OfferID.Tenant != s.TenantID || s.Target.OfferID.Product == "" ||
		s.Target.SaleID != "" || s.Target.StoreID != "" || s.Position < 1 || s.Price.Kopecks() <= 0 {
		return ErrInvalid
	}
	if s.PlacementCode == "category_slot" && s.Target.CategoryID == "" {
		return ErrOfferWithoutCategory
	}
	return s.Period.ValidateStart(now, location)
}

// RestoreCampaign восстанавливает исходный статус и версию для условного UPDATE.
func RestoreCampaign(s CampaignState) *Campaign { return &Campaign{state: s, origin: s} }

// State возвращает отделённый от агрегата снимок.
func (c *Campaign) State() CampaignState { return c.state }

// Origin сохраняет прочитанную версию для условного UPDATE.
func (c *Campaign) Origin() CampaignState { return c.origin }

// MarkSaved начинает новый цикл изменений после успешной записи.
func (c *Campaign) MarkSaved() { c.origin = c.state }

// Edit проверяет редактируемое состояние до изменения агрегата.
func (c *Campaign) Edit(input CampaignState, now time.Time, location *time.Location) (bool, error) {
	if c.state.Status != Draft && c.state.Status != Review && c.state.Status != Approved {
		return false, ErrConflict
	}
	if err := validateInput(input, now, location); err != nil {
		return false, err
	}
	if c.state.Status == Approved && strings.TrimSpace(input.Marking.ERID) == "" {
		return false, ErrERIDRequired
	}
	next := c.state
	next.Title = input.Title
	next.PlacementCode = input.PlacementCode
	next.Position = input.Position
	next.Target = input.Target
	next.Period = input.Period
	next.Price = input.Price
	next.Marking = input.Marking
	if next == c.state {
		return false, nil
	}
	c.state = next
	c.changed(now)
	return true, nil
}

// Decide проверяет переход и фиксирует решение без повторного изменения состояния.
func (c *Campaign) Decide(action Action, note, erid string, payment Payment, now time.Time) (bool, error) {
	next := c.state.Status
	switch action {
	case Submit:
		if next == Review {
			return false, nil
		}
		if next != Draft {
			return false, ErrConflict
		}
		next = Review
	case Approve:
		if next == Approved {
			return false, nil
		}
		if next != Review {
			return false, ErrConflict
		}
		if strings.TrimSpace(erid) == "" {
			return false, ErrERIDRequired
		}
		c.state.Marking.ERID = strings.TrimSpace(erid)
		next = Approved
	case Reject:
		if next == Rejected {
			return false, nil
		}
		if next != Review {
			return false, ErrConflict
		}
		if strings.TrimSpace(note) == "" {
			return false, ErrInvalid
		}
		c.state.RejectionReason = strings.TrimSpace(note)
		next = Rejected
	case Stop:
		if next == Stopped {
			return false, nil
		}
		if next != Review && next != Approved && next != Active {
			return false, ErrConflict
		}
		next = Stopped
	case Finish:
		if next == Finished {
			return false, nil
		}
		if next != Active || now.Format(time.DateOnly) <= c.state.Period.To {
			return false, ErrConflict
		}
		next = Finished
	case Pay:
		if next != Review && next != Approved && next != Active {
			return false, ErrConflict
		}
		if payment.Amount.Kopecks() <= 0 {
			return false, ErrInvalid
		}
		if _, err := time.Parse(time.DateOnly, payment.PaidOn); err != nil {
			return false, ErrInvalid
		}
		payment.Status = Paid
		if payment == c.state.Payment {
			return false, nil
		}
		c.state.Payment = payment
	default:
		return false, ErrActionUnsupported
	}
	c.state.Status = next
	c.changed(now)
	return true, nil
}

// Tick не активирует неоплаченные кампании и не показывает уже истёкшие.
func (c *Campaign) Tick(now time.Time, location *time.Location) bool {
	before := c.state.Status
	day := now.In(location).Format(time.DateOnly)
	if c.state.Status == Approved && c.state.Payment.Status == Paid && day >= c.state.Period.From {
		c.state.Status = Active
	} else if c.state.Status != Active {
		return false
	}
	if day > c.state.Period.To {
		c.state.Status = Finished
	} else if c.state.Status == before {
		return false
	}
	c.changed(now)
	return true
}

func (c *Campaign) changed(now time.Time) {
	c.state.Version = c.origin.Version + 1
	if c.state.Version < 1 {
		c.state.Version = 1
	}
	c.state.UpdatedAt = now
}

// Occupies учитывает резерв заявки до завершения или остановки.
func Occupies(status Status) bool { return status == Review || status == Approved || status == Active }
