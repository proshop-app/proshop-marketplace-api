package participant

import (
	"bytes"
	"encoding/json"
	"strings"
	"time"
	"unicode"

	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

const maxSlugLength = 128

// TenantStatus — статус магазина в реестре платформы.
type TenantStatus string

const (
	// TenantCreating — магазин создаётся.
	TenantCreating TenantStatus = "creating"
	// TenantActive — магазин работает.
	TenantActive TenantStatus = "active"
	// TenantSuspended — магазин приостановлен платформой.
	TenantSuspended TenantStatus = "suspended"
	// TenantDeleting — магазин удаляется.
	TenantDeleting TenantStatus = "deleting"
	// TenantDeleted — магазин удалён; конечный статус зеркала.
	TenantDeleted TenantStatus = "deleted"
)

// ParseTenantStatus проверяет статус из события реестра.
func ParseTenantStatus(value string) (TenantStatus, error) {
	switch status := TenantStatus(value); status {
	case TenantCreating, TenantActive, TenantSuspended, TenantDeleting, TenantDeleted:
		return status, nil
	default:
		return "", ErrInvalidRegistryEvent.WithMessage("неизвестный статус магазина: " + value)
	}
}

// RegistryEvent — событие реестра магазинов платформы: снимок записи на момент At.
type RegistryEvent struct {
	TenantID ids.TenantID
	Slug     string
	Status   TenantStatus
	Cell     string
	Plan     json.RawMessage
	Hosts    json.RawMessage
	At       time.Time
}

// TenantState — плоский снимок записи зеркала.
type TenantState struct {
	TenantID  ids.TenantID
	Slug      string
	Status    TenantStatus
	Cell      string
	Plan      json.RawMessage
	Hosts     json.RawMessage
	EventAt   time.Time
	Version   int64
	UpdatedAt time.Time
}

// Tenant — запись зеркала реестра магазинов платформы.
type Tenant struct {
	state         TenantState
	originVersion int64
}

// NewTenant заводит запись зеркала из первого увиденного события.
func NewTenant(event RegistryEvent, now time.Time) (*Tenant, error) {
	if err := event.validate(); err != nil {
		return nil, err
	}

	tenant := &Tenant{}
	tenant.assign(event, now)

	return tenant, nil
}

// RestoreTenant поднимает запись из хранилища.
func RestoreTenant(state TenantState) *Tenant {
	return &Tenant{state: state, originVersion: state.Version}
}

// Apply применяет событие. Событие старше записи и любое событие после
// удаления не меняют её; повтор того же снимка — тоже.
func (t *Tenant) Apply(event RegistryEvent, now time.Time) (bool, error) {
	if err := event.validate(); err != nil {
		return false, err
	}
	if event.TenantID != t.state.TenantID {
		return false, ErrInvalidRegistryEvent.WithMessage("событие другого магазина")
	}
	if t.state.Status == TenantDeleted || event.At.Before(t.state.EventAt) || t.sameAs(event) {
		return false, nil
	}

	t.assign(event, now)

	return true, nil
}

func (t *Tenant) assign(event RegistryEvent, now time.Time) {
	t.state.TenantID = event.TenantID
	t.state.Slug = strings.TrimSpace(event.Slug)
	t.state.Status = event.Status
	t.state.Cell = event.Cell
	t.state.Plan = event.Plan
	t.state.Hosts = event.Hosts
	t.state.EventAt = event.At
	t.state.Version = t.originVersion + 1
	t.state.UpdatedAt = now
}

func (t *Tenant) sameAs(event RegistryEvent) bool {
	return t.state.EventAt.Equal(event.At) &&
		t.state.Slug == strings.TrimSpace(event.Slug) &&
		t.state.Status == event.Status &&
		t.state.Cell == event.Cell &&
		bytes.Equal(t.state.Plan, event.Plan) &&
		bytes.Equal(t.state.Hosts, event.Hosts)
}

// State — снимок для хранения.
func (t *Tenant) State() TenantState { return t.state }

// OriginVersion — версия на момент чтения; у новой записи — ноль.
func (t *Tenant) OriginVersion() int64 { return t.originVersion }

// MarkSaved фиксирует сохранённую версию.
func (t *Tenant) MarkSaved() { t.originVersion = t.state.Version }

// ID — магазин.
func (t *Tenant) ID() ids.TenantID { return t.state.TenantID }

// Slug — слаг магазина.
func (t *Tenant) Slug() string { return t.state.Slug }

// Status — статус в реестре.
func (t *Tenant) Status() TenantStatus { return t.state.Status }

// Version — версия записи.
func (t *Tenant) Version() int64 { return t.state.Version }

// Joinable — магазин можно сделать участником: он не удаляется.
func (t *Tenant) Joinable() bool {
	return t.state.Status != TenantDeleting && t.state.Status != TenantDeleted
}

func (e RegistryEvent) validate() error {
	slug := strings.TrimSpace(e.Slug)
	if slug == "" || len(slug) > maxSlugLength || strings.IndexFunc(slug, func(r rune) bool {
		return unicode.IsSpace(r) || unicode.IsControl(r)
	}) >= 0 {
		return ErrInvalidRegistryEvent.WithMessage("некорректный слаг магазина")
	}
	if e.At.IsZero() {
		return ErrInvalidRegistryEvent.WithMessage("у события нет времени")
	}
	if _, err := ParseTenantStatus(string(e.Status)); err != nil {
		return err
	}

	return nil
}
