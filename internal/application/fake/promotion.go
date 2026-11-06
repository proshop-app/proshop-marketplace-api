package fake

import (
	"context"
	"maps"
	"slices"
	"sort"

	"github.com/proshop-app/proshop-marketplace/internal/domain/promotion"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

type promotionCounterKey struct {
	id       ids.UUID
	day      string
	position int
}

type promotionViewKey struct {
	id      ids.UUID
	day     string
	visitor string
}

type promotionData struct {
	campaigns  map[ids.UUID]promotion.CampaignState
	placements map[string]promotion.PlacementState
	decisions  map[ids.UUID][]promotion.Decision
	counters   map[promotionCounterKey]promotion.Counter
	views      map[promotionViewKey]bool
}

type promotionRepo struct{ d *promotionData }

func newPromotionData() *promotionData {
	return &promotionData{
		campaigns:  map[ids.UUID]promotion.CampaignState{},
		placements: map[string]promotion.PlacementState{},
		decisions:  map[ids.UUID][]promotion.Decision{},
		counters:   map[promotionCounterKey]promotion.Counter{},
		views:      map[promotionViewKey]bool{},
	}
}

func (d *promotionData) clone() *promotionData {
	result := newPromotionData()
	result.campaigns = maps.Clone(d.campaigns)
	for code, p := range d.placements {
		result.placements[code] = promotion.RestorePlacement(p).State()
	}
	for id, decisions := range d.decisions {
		result.decisions[id] = slices.Clone(decisions)
	}
	result.counters = maps.Clone(d.counters)
	result.views = maps.Clone(d.views)
	return result
}

// SeedPromotionPlacement копирует прайс для изоляции тестового хранилища.
func (s *Store) SeedPromotionPlacement(p promotion.PlacementState) {
	s.read(func(d *data) { d.promotion.placements[p.Code] = promotion.RestorePlacement(p).State() })
}

// SeedPromotion задаёт исходное состояние размещения в тесте.
func (s *Store) SeedPromotion(c promotion.CampaignState) {
	s.read(func(d *data) { d.promotion.campaigns[c.ID] = c })
}

// LockPlacement сериализует проверку ёмкости и запись заявки.
func (r promotionRepo) LockPlacement(ctx context.Context, code string) error {
	_, err := r.Placement(ctx, code)
	return err
}

// Placement читает исходный снимок прайса.
func (r promotionRepo) Placement(_ context.Context, code string) (*promotion.Placement, error) {
	p, ok := r.d.placements[code]
	if !ok {
		return nil, promotion.ErrNotFound
	}
	return promotion.RestorePlacement(p), nil
}

// Placements возвращает прайс с фильтром доступных мест.
func (r promotionRepo) Placements(_ context.Context) ([]promotion.PlacementState, error) {
	result := []promotion.PlacementState{}
	for _, p := range r.d.placements {
		result = append(result, promotion.RestorePlacement(p).State())
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Code < result[j].Code })
	return result, nil
}

// SavePlacement обнаруживает параллельное изменение прайса.
func (r promotionRepo) SavePlacement(_ context.Context, p *promotion.Placement) error {
	state := p.State()
	if current, ok := r.d.placements[state.Code]; !ok || current.Version != p.OriginVersion() {
		return promotion.ErrConflict
	}
	r.d.placements[state.Code] = state
	p.MarkSaved()
	return nil
}

// Get читает размещение с проверкой области доступа.
func (r promotionRepo) Get(_ context.Context, id ids.UUID) (*promotion.Campaign, error) {
	s, ok := r.d.campaigns[id]
	if !ok {
		return nil, promotion.ErrNotFound
	}
	return promotion.RestoreCampaign(s), nil
}

// Save записывает агрегат условно по исходным статусу и версии.
func (r promotionRepo) Save(_ context.Context, c *promotion.Campaign) error {
	s, origin := c.State(), c.Origin()
	current, ok := r.d.campaigns[s.ID]
	if (origin.Version == 0 && ok) ||
		(origin.Version != 0 && (!ok || current.Version != origin.Version || current.Status != origin.Status)) {
		return promotion.ErrConflict
	}
	r.d.campaigns[s.ID] = s
	c.MarkSaved()
	return nil
}

// List возвращает страницу размещений и полное число совпадений.
func (r promotionRepo) List(_ context.Context, f promotion.Filter) ([]promotion.CampaignState, int, error) {
	result := []promotion.CampaignState{}
	for _, s := range r.d.campaigns {
		if (f.Status != "" && s.Status != f.Status) || (f.Kind != "" && s.Kind != f.Kind) ||
			(f.TenantID != "" && s.TenantID != f.TenantID) ||
			(f.PlacementCode != "" && s.PlacementCode != f.PlacementCode) ||
			(f.From != "" && s.Period.To < f.From) || (f.To != "" && s.Period.From > f.To) {
			continue
		}
		result = append(result, s)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	total := len(result)
	start := min(f.Offset, total)
	end := total
	if f.Limit > 0 {
		end = min(total, start+f.Limit)
	}
	return result[start:end], total, nil
}

// Occupancy считает занятые недели без редактируемого размещения.
func (r promotionRepo) Occupancy(
	_ context.Context,
	s promotion.CampaignState,
	scope promotion.Scope,
	weeks []string,
) ([]promotion.Week, error) {
	result := []promotion.Week{}
	for _, from := range weeks {
		week := promotion.Week{From: from}
		for _, other := range r.d.campaigns {
			if other.ID != s.ID && other.PlacementCode == s.PlacementCode && other.Position == s.Position &&
				(scope == promotion.Global || other.Target.CategoryID == s.Target.CategoryID) &&
				promotion.Occupies(other.Status) && other.Period.From <= from && other.Period.To >= from {
				week.Sold++
			}
		}
		result = append(result, week)
	}
	return result, nil
}

// AddDecision добавляет запись аудита в транзакции перехода.
func (r promotionRepo) AddDecision(_ context.Context, id ids.UUID, d promotion.Decision) error {
	r.d.decisions[id] = append(r.d.decisions[id], d)
	return nil
}

// Decisions возвращает журнал в порядке решений.
func (r promotionRepo) Decisions(_ context.Context, id ids.UUID) ([]promotion.Decision, error) {
	return append([]promotion.Decision{}, r.d.decisions[id]...), nil
}

// Count дедуплицирует показ и увеличивает счётчик в одной транзакции.
func (r promotionRepo) Count(
	_ context.Context,
	s promotion.CampaignState,
	day, visitor, kind string,
) (bool, error) {
	key := promotionCounterKey{s.ID, day, s.Position}
	count := r.d.counters[key]
	count.Day, count.Position = day, s.Position
	switch kind {
	case "view":
		view := promotionViewKey{s.ID, day, visitor}
		if r.d.views[view] {
			return false, nil
		}
		r.d.views[view] = true
		count.Impressions++
	case "click":
		count.Clicks++
	default:
		return false, promotion.ErrInvalid
	}
	r.d.counters[key] = count
	return true, nil
}

// Stats читает суточные счётчики за указанный период.
func (r promotionRepo) Stats(_ context.Context, id ids.UUID, from, to string) ([]promotion.Counter, error) {
	result := []promotion.Counter{}
	for key, counter := range r.d.counters {
		if key.id == id && (from == "" || key.day >= from) && (to == "" || key.day <= to) {
			result = append(result, counter)
		}
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Day == result[j].Day {
			return result[i].Position < result[j].Position
		}
		return result[i].Day < result[j].Day
	})
	return result, nil
}
