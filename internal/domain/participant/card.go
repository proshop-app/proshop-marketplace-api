package participant

import (
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	// CardLogo — ключ логотипа в постмодерации.
	CardLogo = "logo"

	// ShowcasePinned — режим с закреплёнными товарами и автоматическим дополнением.
	ShowcasePinned = "pinned"

	// ActionCardHide — запись журнала о скрытии пункта карточки.
	ActionCardHide = "card_hide"

	// ActionCardShow — запись журнала о восстановлении пункта карточки.
	ActionCardShow = "card_show"

	// ActorSystem — автоматическое решение при изменении скрытого значения.
	ActorSystem = "system"

	// NeutralCardColor — запасной цвет, если сервер не прислал фирменный.
	NeutralCardColor = "#333333"
)

// Returns — условия возврата в снимке настроек.
type Returns struct {
	Days     int    `json:"days"`
	Shipping string `json:"shipping"`
	How      string `json:"how"`
}

// Profile — профиль принадлежит серверу, маркетплейс хранит его снимок.
type Profile struct {
	Name     string      `json:"name"`
	Logo     *string     `json:"logo"`
	Color    *BrandColor `json:"color"`
	Showcase Showcase    `json:"showcase"`
}

// BrandColor — содержит готовый цвет карточки, чтобы не пересчитывать контраст.
type BrandColor struct {
	Value    string      `json:"value"`
	Card     string      `json:"card"`
	Contrast json.Number `json:"contrast"`
}

// Showcase — сохраняет порядок закреплений магазина.
type Showcase struct {
	Mode       string   `json:"mode"`
	ProductIDs []string `json:"productIds"`
}

// CardReadiness — серверный чеклист без повторного вычисления на площадке.
type CardReadiness struct {
	Ready   bool     `json:"ready"`
	Missing []string `json:"missing"`
}

// CardHidden — основание скрытия, доступное магазину до исправления поля.
type CardHidden struct {
	Key    string    `json:"key"`
	Reason string    `json:"reason"`
	At     time.Time `json:"at"`
}

// StoreCard — хранит исходные значения отдельно от операторского скрытия.
type StoreCard struct {
	Profile   Profile       `json:"profile"`
	Readiness CardReadiness `json:"card"`
	Returns   *Returns      `json:"returns"`
	Hidden    []CardHidden  `json:"hidden"`
}

// Clone отделяет изменяемые списки от снимка агрегата.
func (c StoreCard) Clone() StoreCard {
	c.Profile.Showcase.ProductIDs = slices.Clone(c.Profile.Showcase.ProductIDs)
	if c.Profile.Color != nil {
		color := *c.Profile.Color
		c.Profile.Color = &color
	}
	if c.Profile.Logo != nil {
		logo := *c.Profile.Logo
		c.Profile.Logo = &logo
	}
	c.Readiness.Missing = slices.Clone(c.Readiness.Missing)
	if c.Returns != nil {
		value := *c.Returns
		c.Returns = &value
	}
	c.Hidden = slices.Clone(c.Hidden)
	return c
}

// ApplyStoreCard доверяет серверному чеклисту: повторный расчёт создал бы два источника истины.
func (p *Participant) ApplyStoreCard(
	profile Profile,
	card CardReadiness,
	returns *Returns,
	now time.Time,
) (bool, error) {
	if name, err := normalizeName(profile.Name); err == nil {
		profile.Name = name
	} else {
		profile.Name = p.Name()
	}
	next := StoreCard{Profile: profile, Readiness: card, Returns: returns, Hidden: p.state.StoreCard.Hidden}
	next = next.Clone()
	next.Hidden = slices.DeleteFunc(next.Hidden, func(h CardHidden) bool {
		return h.Key == CardLogo && !reflect.DeepEqual(profile.Logo, p.state.StoreCard.Profile.Logo)
	})
	changed := !reflect.DeepEqual(next, p.state.StoreCard)
	renamed, err := p.Rename(profile.Name, now)
	if err != nil {
		return false, err
	}
	changed = changed || renamed
	if changed {
		p.state.StoreCard = next
		p.touch(now)
	}
	return changed, nil
}

// CardReady не пересчитывает серверный чеклист.
func (p *Participant) CardReady() bool { return p.state.StoreCard.Readiness.Ready }

// HideCard не меняет готовность: скрытие и заполненность независимы.
func (p *Participant) HideCard(key, reason string, now time.Time) (bool, error) {
	if key != CardLogo {
		return false, ErrInvalidCardKey
	}
	reason = strings.TrimSpace(reason)
	if reason == "" || utf8.RuneCountInString(reason) > 2000 {
		return false, ErrInvalidNote
	}
	for i, h := range p.state.StoreCard.Hidden {
		if h.Key != key {
			continue
		}
		if h.Reason == reason {
			return false, nil
		}
		p.state.StoreCard.Hidden[i] = CardHidden{Key: key, Reason: reason, At: now.UTC()}
		p.touch(now)
		return true, nil
	}
	p.state.StoreCard.Hidden = append(p.state.StoreCard.Hidden, CardHidden{Key: key, Reason: reason, At: now.UTC()})
	p.touch(now)
	return true, nil
}

// ShowCard снимает запрет без изменения исходного значения.
func (p *Participant) ShowCard(key string, now time.Time) (bool, error) {
	if key != CardLogo {
		return false, ErrInvalidCardKey
	}
	before := len(p.state.StoreCard.Hidden)
	p.state.StoreCard.Hidden = slices.DeleteFunc(p.state.StoreCard.Hidden, func(h CardHidden) bool {
		return h.Key == key
	})
	changed := before != len(p.state.StoreCard.Hidden)
	if changed {
		p.touch(now)
	}
	return changed, nil
}

// PublicProfile применяет скрытие перед выдачей покупателю.
func (p *Participant) PublicProfile() Profile {
	profile := p.state.StoreCard.Clone().Profile
	for _, h := range p.state.StoreCard.Hidden {
		if h.Key == CardLogo {
			profile.Logo = nil
		}
	}
	return profile
}

// FieldValue — исходное значение для журнала постмодерации.
func (p Profile) FieldValue(key string) string {
	if key == CardLogo && p.Logo != nil {
		return *p.Logo
	}
	return ""
}
