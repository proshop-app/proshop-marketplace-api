// Package participant — магазины-участники маркетплейса и зеркало реестра
// магазинов платформы.
package participant

import (
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

const maxNameLength = 200

var paymentAccountPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// Origin — состояние агрегата на момент чтения. По нему репозиторий строит
// условное обновление: другой статус или версия в базе — конфликт, а не
// тихая перезапись.
type Origin struct {
	Status  Status
	Version int64
}

// State — плоский снимок агрегата для хранения.
type State struct {
	TenantID         ids.TenantID
	Slug             string
	Name             string
	Status           Status
	Enabled          bool
	PaymentAccountID string
	// ShopEnabled — магазин сам включил участие (marketplace.enabled в его настройках на сервере).
	ShopEnabled bool
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
		TenantID:  tenant.ID(),
		Slug:      tenant.Slug(),
		Name:      clean,
		Status:    StatusOnboarding,
		Version:   1,
		CreatedAt: now,
		UpdatedAt: now,
	}}, nil
}

// Restore поднимает агрегат из хранилища.
func Restore(state State) *Participant {
	return &Participant{state: state, origin: Origin{Status: state.Status, Version: state.Version}}
}

// State — снимок для хранения.
func (p *Participant) State() State { return p.state }

// Origin — статус и версия на момент чтения; у нового агрегата версия нулевая.
func (p *Participant) Origin() Origin { return p.origin }

// IsNew — агрегат ещё не сохранён.
func (p *Participant) IsNew() bool { return p.origin.Version == 0 }

// MarkSaved фиксирует сохранённое состояние как исходное для следующего изменения.
func (p *Participant) MarkSaved() {
	p.origin = Origin{Status: p.state.Status, Version: p.state.Version}
}

// TenantID — магазин участника.
func (p *Participant) TenantID() ids.TenantID { return p.state.TenantID }

// Slug — слаг магазина из реестра.
func (p *Participant) Slug() string { return p.state.Slug }

// Name — название участника в маркетплейсе.
func (p *Participant) Name() string { return p.state.Name }

// Status — подключение к приёму оплаты.
func (p *Participant) Status() Status { return p.state.Status }

// Enabled — оператор включил участника.
func (p *Participant) Enabled() bool { return p.state.Enabled }

// PaymentAccountID — магазин продавца у платёжного провайдера; пусто — не привязан.
func (p *Participant) PaymentAccountID() string { return p.state.PaymentAccountID }

// Version — версия состояния.
func (p *Participant) Version() int64 { return p.state.Version }

// ShopEnabled — магазин сам включил участие в маркетплейсе.
func (p *Participant) ShopEnabled() bool { return p.state.ShopEnabled }

// Subsidy — субсидия доставки продавца.
func (p *Participant) Subsidy() Subsidy { return p.state.Subsidy }

// ApplyShopSettings переносит настройки магазина с сервера. Версия растёт,
// только если поменялось видимое: флаг участия или субсидия; время сверки
// обновляется всегда.
func (p *Participant) ApplyShopSettings(enabled bool, subsidy Subsidy, now time.Time) (bool, error) {
	if err := subsidy.Validate(); err != nil {
		return false, err
	}

	changed := p.state.ShopEnabled != enabled || p.state.Subsidy != subsidy
	p.state.SettingsSyncedAt = now

	if !changed {
		return false, nil
	}

	p.state.ShopEnabled = enabled
	p.state.Subsidy = subsidy
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

// SetPaymentAccount привязывает магазин продавца у провайдера: onboarding → connected,
// у подключённого — замена идентификатора.
func (p *Participant) SetPaymentAccount(accountID string, now time.Time) (bool, error) {
	clean := strings.TrimSpace(accountID)
	if !paymentAccountPattern.MatchString(clean) {
		return false, ErrInvalidPaymentAccount
	}
	if p.state.Status == StatusConnected && p.state.PaymentAccountID == clean {
		return false, nil
	}

	p.state.PaymentAccountID = clean
	p.state.Status = StatusConnected
	p.touch(now)

	return true, nil
}

// Enable включает участника в каталог. Повтор ничего не меняет.
func (p *Participant) Enable(now time.Time) bool {
	if p.state.Enabled {
		return false
	}

	p.state.Enabled = true
	p.touch(now)

	return true
}

// Disable скрывает участника из каталога. Повтор ничего не меняет.
func (p *Participant) Disable(now time.Time) bool {
	if !p.state.Enabled {
		return false
	}

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
