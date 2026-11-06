// Package participant — магазины-участники маркетплейса и зеркало реестра
// магазинов платформы.
package participant

import (
	"reflect"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/proshop-app/proshop-marketplace/pkg/ids"
	"github.com/proshop-app/proshop-marketplace/pkg/money"
)

const maxNameLength = 200

// Origin — состояние агрегата на момент чтения. По нему репозиторий строит
// условное обновление: другой статус или версия в базе — конфликт, а не
// тихая перезапись.
type Origin struct {
	Stage   Stage
	Version int64
}

// State — плоский снимок агрегата для хранения.
type State struct {
	Trust            string
	ApprovedOffers   int
	TenantID         ids.TenantID
	Slug             string
	Name             string
	Stage            Stage
	Enabled          bool
	FreeDeliveryFrom money.Money
	Delivery         DeliverySettings
	AwaitingReview   bool
	LastDecision     *LastDecision
	// Subsidy — субсидия доставки из настроек магазина.
	Subsidy Subsidy
	// SettingsSyncedAt — когда настройки магазина последний раз прочитаны с сервера; ноль — ещё не читались.
	SettingsSyncedAt time.Time
	Version          int64
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

// Participant — магазин, участвующий в маркетплейсе. Меняется только своими методами.
type Participant struct {
	state  State
	origin Origin
}

// New заводит участника для магазина из реестра. Новый участник скрыт:
// оператор включает его отдельно.
func New(tenant *Tenant, name string, now time.Time) (*Participant, error) {
	if !tenant.Joinable() {
		return nil, ErrTenantUnavailable
	}

	clean, err := normalizeName(name)
	if err != nil {
		return nil, err
	}

	return &Participant{state: State{
		Trust:          Probation,
		TenantID:       tenant.ID(),
		Slug:           tenant.Slug(),
		Name:           clean,
		Stage:          StageCandidate,
		AwaitingReview: true,
		Version:        1,
		CreatedAt:      now,
		UpdatedAt:      now,
	}}, nil
}

// Restore поднимает агрегат из хранилища.
func Restore(state State) *Participant {
	if state.Trust == "" {
		state.Trust = Probation
	}
	if state.LastDecision != nil {
		d := *state.LastDecision
		state.LastDecision = &d
	}
	state.Enabled = state.Stage == StageConnected
	state.Delivery = state.Delivery.Clone()
	return &Participant{state: state, origin: Origin{Stage: state.Stage, Version: state.Version}}
}

// State — снимок для хранения.
func (p *Participant) State() State {
	state := p.state
	if state.LastDecision != nil {
		d := *state.LastDecision
		state.LastDecision = &d
	}
	state.Delivery = state.Delivery.Clone()
	return state
}

// Origin — статус и версия на момент чтения; у нового агрегата версия нулевая.
func (p *Participant) Origin() Origin { return p.origin }

// IsNew — агрегат ещё не сохранён.
func (p *Participant) IsNew() bool { return p.origin.Version == 0 }

// MarkSaved фиксирует сохранённое состояние как исходное для следующего изменения.
func (p *Participant) MarkSaved() {
	p.origin = Origin{Stage: p.state.Stage, Version: p.state.Version}
}

// TenantID — магазин участника.
func (p *Participant) TenantID() ids.TenantID { return p.state.TenantID }

// Slug — слаг магазина из реестра.
func (p *Participant) Slug() string { return p.state.Slug }

// Name — название участника в маркетплейсе.
func (p *Participant) Name() string { return p.state.Name }

// Stage — стадия участия магазина.
func (p *Participant) Stage() Stage { return p.state.Stage }

// Enabled — оператор включил участника.
func (p *Participant) Enabled() bool { return p.state.Enabled }

// Version — версия состояния.
func (p *Participant) Version() int64 { return p.state.Version }

// Subsidy — субсидия доставки продавца.
func (p *Participant) Subsidy() Subsidy { return p.state.Subsidy }

// ApplyShopSettings переносит настройки магазина с сервера. Версия растёт,
// только если поменялись настройки доставки; время сверки
// обновляется всегда.
func (p *Participant) ApplyShopSettings(
	subsidy Subsidy,
	freeDeliveryFrom money.Money,
	delivery DeliverySettings,
	now time.Time,
) (bool, error) {
	if err := subsidy.Validate(); err != nil {
		return false, err
	}

	if freeDeliveryFrom.IsNegative() {
		return false, ErrInvalidDeliverySettings
	}
	changed := p.state.Subsidy != subsidy ||
		p.state.FreeDeliveryFrom != freeDeliveryFrom || !reflect.DeepEqual(p.state.Delivery, delivery)
	p.state.SettingsSyncedAt = now

	if !changed {
		return false, nil
	}

	p.state.Subsidy = subsidy
	p.state.FreeDeliveryFrom = freeDeliveryFrom
	p.state.Delivery = delivery.Clone()
	p.touch(now)

	return true, nil
}

// Rename меняет название. Возвращает, изменилось ли состояние.
func (p *Participant) Rename(name string, now time.Time) (bool, error) {
	clean, err := normalizeName(name)
	if err != nil {
		return false, err
	}
	if clean == p.state.Name {
		return false, nil
	}

	p.state.Name = clean
	p.touch(now)

	return true, nil
}

// Enable включает участника в каталог. Повтор ничего не меняет.
func (p *Participant) Enable(now time.Time) bool {
	if p.state.Enabled {
		return false
	}

	p.state.Stage = StageConnected
	p.state.AwaitingReview = false
	p.state.Enabled = true
	p.touch(now)

	return true
}

// Disable скрывает участника из каталога. Повтор ничего не меняет.
func (p *Participant) Disable(now time.Time) bool {
	if !p.state.Enabled {
		return false
	}

	p.state.Stage = StageDisconnected
	p.state.AwaitingReview = false
	p.state.Enabled = false
	p.touch(now)

	return true
}

// SyncSlug переносит слаг из реестра платформы.
func (p *Participant) SyncSlug(tenant *Tenant, now time.Time) bool {
	if tenant.ID() != p.state.TenantID || tenant.Slug() == p.state.Slug {
		return false
	}

	p.state.Slug = tenant.Slug()
	p.touch(now)

	return true
}

// touch — версия растёт на единицу за сохранение, сколько бы методов ни вызвали.
func (p *Participant) touch(now time.Time) {
	p.state.Version = p.origin.Version + 1
	p.state.UpdatedAt = now
}

func normalizeName(name string) (string, error) {
	clean := strings.TrimSpace(name)
	if clean == "" || utf8.RuneCountInString(clean) > maxNameLength {
		return "", ErrInvalidName
	}

	for _, r := range clean {
		if unicode.IsControl(r) {
			return "", ErrInvalidName
		}
	}

	return clean, nil
}

// SetDelivery меняет покрытие с проверкой версии, включая повтор того же значения.
func (p *Participant) SetDelivery(settings DeliverySettings, version int64, now time.Time) (bool, error) {
	if version != p.state.Version {
		return false, ErrConflict
	}
	if err := settings.Validate(); err != nil {
		return false, err
	}
	if reflect.DeepEqual(p.state.Delivery, settings) {
		return false, nil
	}
	p.state.Delivery = settings.Clone()
	p.touch(now)
	return true, nil
}

// DeliveryValid — обязательные настройки доставки получены и корректны.
func (p *Participant) DeliveryValid() bool {
	s := p.state
	return s.Delivery.Validate() == nil && s.Subsidy.Validate() == nil && s.Subsidy.Enabled &&
		!s.FreeDeliveryFrom.IsNegative()
}

// InQueue — кандидат ожидает решения и готов по настройкам доставки.
func (p *Participant) InQueue() bool {
	return p.state.Stage == StageCandidate && p.state.AwaitingReview && p.DeliveryValid()
}

// Return возвращает кандидата с замечаниями, убирая из очереди.
func (p *Participant) Return(now time.Time) (bool, error) {
	if p.state.Stage != StageCandidate {
		return false, ErrConflict
	}
	p.state.Stage = StageReturned
	p.state.AwaitingReview = false
	p.touch(now)
	return true, nil
}

// Submit возвращает исправленный магазин в кандидаты; повтор конфликтует.
func (p *Participant) Submit(now time.Time) (bool, error) {
	if p.state.Stage != StageReturned {
		return false, ErrConflict
	}
	p.state.Stage = StageCandidate
	p.state.AwaitingReview = true
	p.touch(now)
	return true, nil
}

// RecordDecision сохраняет последнее решение об участии; доверие ведётся отдельно.
func (p *Participant) RecordDecision(d DecisionRecord) {
	p.state.LastDecision = &LastDecision{
		Reason:        d.Reason,
		ReasonMessage: d.ReasonMessage,
		Note:          d.Note,
		At:            d.At,
		Operator:      d.Operator,
	}
	p.touch(d.At)
}
