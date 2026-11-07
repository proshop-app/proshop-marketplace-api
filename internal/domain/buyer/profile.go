package buyer

import (
	"strings"
	"unicode/utf8"

	"github.com/proshop-app/proshop-marketplace/pkg/money"
	"github.com/proshop-app/proshop-marketplace/pkg/regions"
)

const (
	// FavoritesLimit — максимум избранных предложений покупателя.
	FavoritesLimit = 500
	// PriceWatchesLimit — максимум наблюдений за ценой покупателя.
	PriceWatchesLimit = 500
	// SubscriptionsLimit — максимум подписок покупателя на магазины.
	SubscriptionsLimit = 500
)

// Region — канонический регион из справочника доставки.
type Region struct {
	Code string `json:"code"`
	Name string `json:"name"`
}

// Pickup — сохранённый пункт выдачи, проверенный по справочнику.
type Pickup struct {
	Provider   string `json:"provider"`
	Code       string `json:"code"`
	Address    string `json:"address"`
	RegionCode string `json:"regionCode"`
}

// Notifications — предпочтения будущих рассылок; отправка сообщений здесь не выполняется.
type Notifications struct {
	PriceDrops bool `json:"priceDrops"`
	ShopNews   bool `json:"shopNews"`
}

// ProfileState — снимок профиля с версией для условного сохранения.
type ProfileState struct {
	Name          string        `json:"name"`
	Region        *Region       `json:"region"`
	PickupPoint   *Pickup       `json:"pickupPoint"`
	Notifications Notifications `json:"notifications"`
	Version       int64         `json:"-"`
}

// Profile — профиль покупателя; изменения проверяются до замены состояния.
type Profile struct {
	state  ProfileState
	origin int64
}

// RestoreProfile — восстанавливает профиль и отделяет изменяемые указатели.
func RestoreProfile(state ProfileState) *Profile {
	return &Profile{state: cloneProfile(state), origin: state.Version}
}

// State — возвращает независимый снимок.
func (p *Profile) State() ProfileState { return cloneProfile(p.state) }

// OriginVersion — версия при чтении, используемая условным UPDATE.
func (p *Profile) OriginVersion() int64 { return p.origin }

// MarkSaved — фиксирует успешное сохранение версии.
func (p *Profile) MarkSaved() { p.origin = p.state.Version }

// Change — проверяет и применяет изменения; повтор не увеличивает версию.
func (p *Profile) Change(state ProfileState) (bool, error) {
	state.Name = strings.TrimSpace(state.Name)
	if utf8.RuneCountInString(state.Name) > 200 {
		return false, ErrInvalidProfile
	}
	if state.Region != nil {
		name, ok := regions.Name(state.Region.Code)
		if !ok {
			return false, ErrInvalidProfile
		}
		state.Region = &Region{Code: state.Region.Code, Name: name}
	}
	if point := state.PickupPoint; point != nil {
		if point.Provider == "" || point.Code == "" || len(point.Code) > 128 || len(point.Address) > 4000 {
			return false, ErrInvalidProfile
		}
		if point.RegionCode != "" && !regions.ValidRU(point.RegionCode) {
			return false, ErrInvalidProfile
		}
	}
	if sameProfile(p.state, state) {
		return false, nil
	}
	state.Version = p.origin + 1
	p.state = cloneProfile(state)
	return true, nil
}

// SelectPickup — сохраняет выбранный при оформлении пункт.
func (p *Profile) SelectPickup(point Pickup) (bool, error) {
	state := p.State()
	state.PickupPoint = &point
	return p.Change(state)
}

func cloneProfile(s ProfileState) ProfileState {
	if s.Region != nil {
		v := *s.Region
		s.Region = &v
	}
	if s.PickupPoint != nil {
		v := *s.PickupPoint
		s.PickupPoint = &v
	}
	return s
}

func sameProfile(a, b ProfileState) bool {
	if a.Name != b.Name || a.Notifications != b.Notifications {
		return false
	}
	if (a.Region == nil) != (b.Region == nil) || (a.PickupPoint == nil) != (b.PickupPoint == nil) {
		return false
	}
	return (a.Region == nil || *a.Region == *b.Region) &&
		(a.PickupPoint == nil || *a.PickupPoint == *b.PickupPoint)
}

// PriceDropped — проверяет снижение цены с учётом включительной цели.
func PriceDropped(initial, current money.Money, target *money.Money) bool {
	return current.Kopecks() < initial.Kopecks() &&
		(target == nil || current.Kopecks() <= target.Kopecks())
}
