package fake

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/buyer"
	"github.com/proshop-app/proshop-marketplace/internal/domain/checkout"
	"github.com/proshop-app/proshop-marketplace/internal/domain/delivery"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

// IDs — предсказуемые идентификаторы и токены.
type IDs struct {
	mu sync.Mutex
	n  int
}

// Calls — шлюз проверки звонком в памяти.
type Calls struct {
	mu       sync.Mutex
	statuses map[string]buyer.CallStatus
	started  []buyer.Phone
	asked    int
	// StartErr, если задан, возвращается из Start.
	StartErr error
	// StatusErr, если задан, возвращается из Status.
	StatusErr error
}

// Sessions — сессии в памяти.
type Sessions struct {
	mu       sync.Mutex
	sessions map[string]buyer.Session
	// SaveErr, если задан, возвращается из Save.
	SaveErr error
}

// Limiter — лимиты частоты без окна: счётчик на ключ.
type Limiter struct {
	RetryAfter time.Duration
	Err        error
	mu         sync.Mutex
	counts     map[string]int
}

// Platform — API сервера установки в памяти.
type Platform struct {
	Reviews           []ports.Review
	ReviewsErr        error
	buyerProfiles     map[ids.UUID]map[ids.TenantID]string
	buyerItems        map[ids.UUID]map[ids.TenantID]map[ids.ProductID]ports.ItemCheck
	checks            []ItemCheckCall
	mu                sync.Mutex
	items             map[ids.TenantID]map[ids.ProductID]ports.ItemCheck
	quotes            map[ids.TenantID]delivery.Offered
	settings          map[ids.TenantID]ports.ShopSettings
	failing           map[ids.TenantID]error
	requests          []ports.QuoteRequest
	orders            map[string]checkout.SellerOrder
	orderRefusals     map[string]error
	orderTenants      map[string]ids.TenantID
	orderBodies       map[string]string
	orderRequests     map[ids.TenantID][]ports.CreateOrderRequest
	createErrors      map[ids.TenantID]error
	pollErrors        map[ids.TenantID]error
	pollCalls         map[ids.TenantID]int
	MinSubsidyPercent int
	BeforeSettings    func(ids.TenantID)
	OrderNow          time.Time
	AfterCreate       func()
}

// PickupProvider — справочник перевозчика в памяти.
type PickupProvider struct {
	Info    delivery.CarrierInfo
	Batches [][]delivery.PickupPoint
	Err     error
}

// PickupIndex — геоиндекс пунктов в памяти.
type PickupIndex struct {
	mu      sync.Mutex
	points  map[pointKey]delivery.PickupPoint
	created bool
}

// NewUUID — следующий идентификатор вида 00000000-0000-4000-8000-00000000000N.
func (g *IDs) NewUUID() ids.UUID {
	g.mu.Lock()
	defer g.mu.Unlock()

	g.n++

	return ids.UUID(fmt.Sprintf("00000000-0000-4000-8000-%012d", g.n))
}

// NewToken — следующий токен.
func (g *IDs) NewToken() string {
	g.mu.Lock()
	defer g.mu.Unlock()

	g.n++

	return fmt.Sprintf("token-%d", g.n)
}

// NewCalls — шлюз без проверок.
func NewCalls() *Calls {
	return &Calls{statuses: map[string]buyer.CallStatus{}}
}

// Start заводит проверку ref-N.
func (c *Calls) Start(_ context.Context, phone buyer.Phone, _ string) (buyer.CallInstruction, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.StartErr != nil {
		return buyer.CallInstruction{}, c.StartErr
	}

	c.started = append(c.started, phone)
	ref := fmt.Sprintf("ref-%d", len(c.started))
	c.statuses[ref] = buyer.CallPending

	return buyer.CallInstruction{ProviderRef: ref, CallPhone: "78005008275", CallPhonePretty: "+7 (800) 500-8275"}, nil
}

// Status — состояние проверки.
func (c *Calls) Status(_ context.Context, ref string) (buyer.CallStatus, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.asked++
	if c.StatusErr != nil {
		return "", c.StatusErr
	}

	status, ok := c.statuses[ref]
	if !ok {
		return "", errors.New("нет такой проверки")
	}

	return status, nil
}

// Set задаёт состояние проверки.
func (c *Calls) Set(ref string, status buyer.CallStatus) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.statuses[ref] = status
}

// Asked — сколько раз спрашивали состояние.
func (c *Calls) Asked() int {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.asked
}

// NewSessions — пустое хранилище сессий.
func NewSessions() *Sessions {
	return &Sessions{sessions: map[string]buyer.Session{}}
}

// Save сохраняет сессию.
func (s *Sessions) Save(_ context.Context, session buyer.Session) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.SaveErr != nil {
		return s.SaveErr
	}

	s.sessions[session.TokenHash] = session

	return nil
}

// Get — сессия по отпечатку.
func (s *Sessions) Get(_ context.Context, hash string) (buyer.Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	session, ok := s.sessions[hash]
	if !ok {
		return buyer.Session{}, buyer.ErrSessionNotFound
	}

	return session, nil
}

// Delete удаляет сессию.
func (s *Sessions) Delete(_ context.Context, hash string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	delete(s.sessions, hash)

	return nil
}

// Count — число сессий.
func (s *Sessions) Count() int {
	s.mu.Lock()
	defer s.mu.Unlock()

	return len(s.sessions)
}

// NewLimiter — лимиты с нулевыми счётчиками.
func NewLimiter() *Limiter {
	return &Limiter{counts: map[string]int{}}
}

// Allow засчитывает попытку.
func (l *Limiter) Allow(_ context.Context, key string, limit int, window time.Duration) (bool, time.Duration, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.Err != nil {
		return false, 0, l.Err
	}
	l.counts[key]++

	if l.counts[key] > limit {
		if l.RetryAfter > 0 {
			return false, l.RetryAfter, nil
		}
		return false, window, nil
	}

	return true, 0, nil
}

// NewPlatform — пустой сервер.
func NewPlatform() *Platform {
	return &Platform{
		items:             map[ids.TenantID]map[ids.ProductID]ports.ItemCheck{},
		orders:            map[string]checkout.SellerOrder{},
		orderRefusals:     map[string]error{},
		orderTenants:      map[string]ids.TenantID{},
		orderBodies:       map[string]string{},
		orderRequests:     map[ids.TenantID][]ports.CreateOrderRequest{},
		createErrors:      map[ids.TenantID]error{},
		pollErrors:        map[ids.TenantID]error{},
		pollCalls:         map[ids.TenantID]int{},
		MinSubsidyPercent: 30,
		quotes:            map[ids.TenantID]delivery.Offered{},
		settings:          map[ids.TenantID]ports.ShopSettings{},
		failing:           map[ids.TenantID]error{},
	}
}

// SetItem задаёт ответ магазина по позиции.
func (p *Platform) SetItem(tenant ids.TenantID, check ports.ItemCheck) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.items[tenant] == nil {
		p.items[tenant] = map[ids.ProductID]ports.ItemCheck{}
	}

	p.items[tenant][check.ProductID] = check
}

// SetQuote задаёт ответ магазина на расчёт.
func (p *Platform) SetQuote(tenant ids.TenantID, offered delivery.Offered) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.quotes[tenant] = offered
}

// SetSettings задаёт настройки магазина.
func (p *Platform) SetSettings(tenant ids.TenantID, settings ports.ShopSettings) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.settings[tenant] = settings
}

// Fail делает магазин недоступным.
func (p *Platform) Fail(tenant ids.TenantID, err error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.failing[tenant] = err
}

// QuoteRequests — запросы расчёта по порядку.
func (p *Platform) QuoteRequests() []ports.QuoteRequest {
	p.mu.Lock()
	defer p.mu.Unlock()

	return append([]ports.QuoteRequest(nil), p.requests...)
}

// CheckItems — ответы по позициям; неизвестная позиция — не продаётся.
func (p *Platform) CheckItems(
	_ context.Context,
	tenant ids.TenantID,
	request ports.ItemsCheckRequest,
) ([]ports.ItemCheck, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if err := p.failing[tenant]; err != nil {
		return nil, err
	}

	p.checks = append(p.checks, ItemCheckCall{TenantID: tenant, Request: request})
	checks := make([]ports.ItemCheck, 0, len(request.Items))
	for _, item := range request.Items {
		check, ok := p.buyerItems[request.BuyerID][tenant][item.ProductID]
		if !ok {
			check, ok = p.items[tenant][item.ProductID]
		}
		if !ok {
			check = ports.ItemCheck{ProductID: item.ProductID}
		}

		checks = append(checks, check)
	}

	return checks, nil
}

// QuoteDelivery — заданный расчёт; не задан — магазин не везёт этим перевозчиком.
func (p *Platform) QuoteDelivery(
	_ context.Context,
	tenant ids.TenantID,
	request ports.QuoteRequest,
) (delivery.Offered, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.requests = append(p.requests, request)

	if err := p.failing[tenant]; err != nil {
		return delivery.Offered{}, err
	}

	offered, ok := p.quotes[tenant]
	if !ok {
		return delivery.Offered{Reason: delivery.ReasonCarrierNotConnected}, nil
	}

	return offered, nil
}

// MarketplaceSettings — заданные настройки; не заданы — участие выключено.
func (p *Platform) MarketplaceSettings(_ context.Context, tenant ids.TenantID) (ports.ShopSettings, error) {
	if p.BeforeSettings != nil {
		p.BeforeSettings(tenant)
	}
	p.mu.Lock()
	defer p.mu.Unlock()

	if err := p.failing[tenant]; err != nil {
		return ports.ShopSettings{}, err
	}

	return p.settings[tenant], nil
}

// Carrier — перевозчик.
func (p *PickupProvider) Carrier() delivery.CarrierInfo { return p.Info }

// Fetch отдаёт пачки; Err — после первой пачки, как обрыв выгрузки.
func (p *PickupProvider) Fetch(_ context.Context, yield func([]delivery.PickupPoint) error) error {
	for i, batch := range p.Batches {
		if p.Err != nil && i > 0 {
			return p.Err
		}

		if err := yield(append([]delivery.PickupPoint(nil), batch...)); err != nil {
			return err
		}
	}

	return p.Err
}

// NewPickupIndex — пустой индекс.
func NewPickupIndex() *PickupIndex {
	return &PickupIndex{points: map[pointKey]delivery.PickupPoint{}}
}

// Ensure заводит индекс.
func (x *PickupIndex) Ensure(context.Context) (bool, error) {
	x.mu.Lock()
	defer x.mu.Unlock()
	created := !x.created
	x.created = true

	return created, nil
}

// Put пишет действующие и убирает закрытые.
func (x *PickupIndex) Put(_ context.Context, points []delivery.PickupPoint) error {
	x.mu.Lock()
	defer x.mu.Unlock()

	for _, point := range points {
		key := pointKey{point.Carrier, point.Code}
		if point.Active {
			x.points[key] = point
		} else {
			delete(x.points, key)
		}
	}

	return nil
}

// Search — пункты перевозчика в области.
func (x *PickupIndex) Search(_ context.Context, query ports.PickupPointQuery) ([]delivery.PickupPoint, error) {
	x.mu.Lock()
	defer x.mu.Unlock()

	var found []delivery.PickupPoint

	for _, point := range x.points {
		if point.Carrier != query.Carrier || len(found) == query.Limit {
			continue
		}
		box := query.Area.BBox
		switch {
		case box != nil && point.Location.Lat >= box.MinLat && point.Location.Lat <= box.MaxLat &&
			point.Location.Lon >= box.MinLon && point.Location.Lon <= box.MaxLon:
			found = append(found, point)
		case box == nil && strings.EqualFold(point.City, strings.TrimSpace(query.Area.City)):
			found = append(found, point)
		}
	}

	return found, nil
}

// Size — число пунктов в индексе.
func (x *PickupIndex) Size() int {
	x.mu.Lock()
	defer x.mu.Unlock()

	return len(x.points)
}
